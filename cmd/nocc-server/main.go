package main

import (
	"fmt"
	"net"
	"os"
	"path"
	"time"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/VKCOM/nocc/internal/server"
	"github.com/VKCOM/nocc/pb"
	"google.golang.org/grpc"
)

func failedStart(message string, err error) {
	_, _ = fmt.Fprintln(os.Stderr, fmt.Sprint("failed to start nocc-server: ", message, ": ", err))
	os.Exit(1)
}

// cleanupWorkingDir ensures that workingDir exists and is empty
// it's executed on server launch
// as a consequence, all file caches are lost on restart
func cleanupWorkingDir(workingDir string) error {
	oldWorkingDir := workingDir + ".old"

	if err := os.RemoveAll(oldWorkingDir); err != nil {
		failedStart("can't remove old working dir", err)
	}
	if _, err := os.Stat(workingDir); err == nil {
		if err := os.Rename(workingDir, oldWorkingDir); err != nil {
			failedStart("can't rename working dir %s to .old", err)
		}
	}
	if err := os.MkdirAll(workingDir, os.ModePerm); err != nil {
		return err
	}
	return nil
}

// printDockerContainerIP is a dev/debug function called only when build special for local Docker, for local testing.
// As Docker containers' IP often change, this info at start up is useful for development.
func printDockerContainerIP() {
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		fmt.Println("running in Docker container IP", conn.LocalAddr().(*net.UDPAddr).IP.String())
		_ = conn.Close()
	}
}

func main() {
	var err error

	showVersionAndExit := common.CmdEnvBool("Show version and exit", false,
		"version", "")
	showVersionAndExitShort := common.CmdEnvBool("Show version and exit", false,
		"v", "")
	bindHost := common.CmdEnvString("Binding address, default 0.0.0.0.", "0.0.0.0",
		"host", "")
	listenPort := common.CmdEnvInt("Listening port, default 43210.", 43210,
		"port", "")
	workingDir := common.CmdEnvString("Directory for saving incoming files, default /tmp/nocc-server.", "/tmp/nocc-server",
		"working-dir", "")
	logFileName := common.CmdEnvString("A filename to log, by default use stderr.", "",
		"log-filename", "")
	logVerbosity := common.CmdEnvInt("Logger verbosity level for INFO (-1 off, default 0, max 2).\nErrors are logged always.", 0,
		"log-verbosity", "")
	srcCacheLimit := common.CmdEnvInt("Header and source cache limit, in bytes, default 4G.", 4*1024*1024*1024,
		"src-cache-limit", "")
	objCacheLimit := common.CmdEnvInt("Compiled obj cache limit, in bytes, default 16G.", 16*1024*1024*1024,
		"obj-cache-limit", "")
	statsdHostPort := common.CmdEnvString("Statsd udp address (host:port), omitted by default.\nIf omitted, stats won't be written.", "",
		"statsd", "")

	common.ParseCmdFlagsCombiningWithEnv()

	if *showVersionAndExit || *showVersionAndExitShort {
		fmt.Println(common.GetVersion())
		os.Exit(0)
	}

	if err = cleanupWorkingDir(*workingDir); err != nil {
		failedStart("Can't create working directory "+*workingDir, err)
	}

	if err = server.MakeLoggerServer(*logFileName, *logVerbosity); err != nil {
		failedStart("Can't init logger", err)
	}

	s := &server.NoccServer{
		StartTime: time.Now(),
	}

	s.Stats, err = server.MakeStatsd(*statsdHostPort)
	if err != nil {
		failedStart("Failed to connect to statsd", err)
	}

	s.ActiveClients, err = server.MakeClientsStorage(path.Join(*workingDir, "clients"))
	if err != nil {
		failedStart("Failed to init clients hashtable", err)
	}

	s.CxxLauncher, err = server.MakeCxxLauncher()
	if err != nil {
		failedStart("Failed to init cxx launcher", err)
	}

	s.SystemHeaders, err = server.MakeSystemHeadersCache()
	if err != nil {
		failedStart("Failed to init system headers hashtable", err)
	}

	s.SrcFileCache, err = server.MakeSrcFileCache(path.Join(*workingDir, "src-cache"), *srcCacheLimit)
	if err != nil {
		failedStart("Failed to init src file cache", err)
	}

	s.ObjFileCache, err = server.MakeObjFileCache(path.Join(*workingDir, "obj-cache"), *objCacheLimit)
	if err != nil {
		failedStart("Failed to init obj file cache", err)
	}

	s.PchCompilation, err = server.MakePchCompilation(path.Join(*workingDir, "pch"))
	if err != nil {
		failedStart("Failed to init pch compilation", err)
	}

	s.GRPCServer = grpc.NewServer()
	pb.RegisterCompilationServiceServer(s.GRPCServer, s)

	s.Cron, err = server.MakeCron(s)
	if err != nil {
		failedStart("Failed to init cron", err)
	}

	if common.GetVersion() == "docker" {
		printDockerContainerIP()
	}

	listener, err := s.StartGRPCListening(fmt.Sprintf("%s:%d", *bindHost, *listenPort))
	if err != nil {
		failedStart("Failed to listen", err)
	}

	s.GRPCServer.Stop()
	_ = listener.Close()
}

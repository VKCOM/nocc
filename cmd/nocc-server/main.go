package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
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

// prepareEmptyDir ensures that serverDir exists and is empty
// it's executed on server launch
// as a consequence, all file caches are lost on restart
func prepareEmptyDir(parentDir *string, subdir string) string {
	// if /tmp/nocc/cpp/src-cache already exists, it means, that it contains files from a previous launch
	// to start up as quickly as possible, do the following:
	// 1) rename it to /tmp/nocc/cpp/src-cache.old
	// 2) clear it recursively in the background
	serverDir := *parentDir + "/" + subdir
	if _, err := os.Stat(serverDir); err == nil {
		oldDirRenamed := fmt.Sprintf("%s.old.%d", serverDir, time.Now().Unix())
		if err := os.Rename(serverDir, oldDirRenamed); err != nil {
			failedStart("can't rename "+serverDir, err)
		}
		go func() {
			_ = os.RemoveAll(oldDirRenamed)
		}()
	}

	if err := os.MkdirAll(serverDir, os.ModePerm); err != nil {
		failedStart("can't create "+serverDir, err)
	}
	return serverDir
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
	cppStoreDir := common.CmdEnvString("Directory for incoming C++ files and src cache, default /tmp/nocc/cpp.\nIt can be placed in tmpfs to speed up compilation", "/tmp/nocc/cpp",
		"cpp-dir", "")
	objStoreDir := common.CmdEnvString("Directory for resulting obj files and obj cache, default /tmp/nocc/obj.", "/tmp/nocc/obj",
		"obj-dir", "")
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
	maxParallelCxx := common.CmdEnvInt("Max amount of C++ compiler processes launched in parallel, other ready sessions are waiting in a queue.\nBy default, it's a number of CPUs on the current machine.", int64(runtime.NumCPU()),
		"max-parallel-cxx", "")
	checkInactiveTimeout := common.CmdEnvDuration("The time since the last activity, after which the server will consider the client to be inactive. By default, it's 5 minutes.", 5*time.Minute,
		"check-inactive-timeout", "")

	common.ParseCmdFlagsCombiningWithEnv()

	if *showVersionAndExit || *showVersionAndExitShort {
		fmt.Println(common.GetVersion())
		os.Exit(0)
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

	s.ActiveClients, err = server.MakeClientsStorage(prepareEmptyDir(cppStoreDir, "clients"), *checkInactiveTimeout)
	if err != nil {
		failedStart("Failed to init clients hashtable", err)
	}

	s.CxxLauncher, err = server.MakeCxxLauncher(*maxParallelCxx)
	if err != nil {
		failedStart("Failed to init cxx launcher", err)
	}

	s.SystemHeaders, err = server.MakeSystemHeadersCache()
	if err != nil {
		failedStart("Failed to init system headers hashtable", err)
	}

	s.SrcFileCache, err = server.MakeSrcFileCache(prepareEmptyDir(cppStoreDir, "src-cache"), *srcCacheLimit)
	if err != nil {
		failedStart("Failed to init src file cache", err)
	}

	s.ObjFileCache, err = server.MakeObjFileCache(prepareEmptyDir(objStoreDir, "obj-cache"), prepareEmptyDir(objStoreDir, "cxx-out"), *objCacheLimit)
	if err != nil {
		failedStart("Failed to init obj file cache", err)
	}

	s.PchCompilation, err = server.MakePchCompilation(prepareEmptyDir(cppStoreDir, "pch"))
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

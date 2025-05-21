package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/VKCOM/nocc/internal/client"
	"github.com/VKCOM/nocc/internal/common"
)

func failedStart(err interface{}) {
	_, _ = fmt.Fprintln(os.Stderr, "[nocc]", err)
	os.Exit(1)
}

func failedStartDaemon(err interface{}) {
	_, _ = fmt.Fprintln(os.Stdout, "daemon not started:", err)
	os.Exit(1)
}

func readNoccServersFile(envNoccServersFilename string) (remoteNoccHosts []string) {
	contents, err := os.ReadFile(envNoccServersFilename)
	if err != nil {
		failedStart(err)
	}
	lines := bytes.Split(contents, []byte{'\n'})
	remoteNoccHosts = make([]string, 0, len(lines))

	for _, line := range lines {
		hostAndComment := bytes.SplitN(bytes.TrimSpace(line), []byte{'#'}, 2)
		if len(hostAndComment) > 0 && len(hostAndComment[0]) > 0 {
			trimmedHost := string(bytes.Trim(hostAndComment[0], " ;,"))
			remoteNoccHosts = append(remoteNoccHosts, trimmedHost)
		}
	}
	return
}

func parseNoccServersEnv(envNoccServers string) (remoteNoccHosts []string) {
	hosts := strings.Split(envNoccServers, ";")
	remoteNoccHosts = make([]string, 0, len(hosts))
	for _, host := range hosts {
		if trimmedHost := strings.TrimSpace(host); len(trimmedHost) != 0 {
			remoteNoccHosts = append(remoteNoccHosts, trimmedHost)
		}
	}
	return
}

func main() {
	showVersionAndExit := common.CmdEnvBool("Show version and exit.", false,
		"version", "")
	showVersionAndExitShort := common.CmdEnvBool("Show version and exit.", false,
		"v", "")
	checkServersAndExit := common.CmdEnvBool("Print out servers status and exit.", false,
		"check-servers", "")
	dumpServerLogsAndExit := common.CmdEnvBool("Dump logs from all servers to /tmp/nocc-dump-logs/ and exit.\nServers must be launched with the `-log-filename` option.", false,
		"dump-server-logs", "")
	dropServerCachesAndExit := common.CmdEnvBool("Drop src cache and obj cache on all servers and exit.", false,
		"drop-server-caches", "")
	noccServers := common.CmdEnvString("Remote nocc servers — a list of 'host:port' delimited by ';'.\nIf not set, nocc will read NOCC_SERVERS_FILENAME.", "",
		"", "NOCC_SERVERS")
	noccServersFilename := common.CmdEnvString("A file with nocc servers — a list of 'host:port', one per line (with optional comments starting with '#').\nUsed if NOCC_SERVERS is unset.", "",
		"", "NOCC_SERVERS_FILENAME")
	logFileName := common.CmdEnvString("A filename to log, nothing by default.\nErrors are duplicated to stderr always.", "",
		"", "NOCC_LOG_FILENAME")
	logVerbosity := common.CmdEnvInt("Logger verbosity level for INFO (-1 off, default 0, max 2).\nErrors are logged always.", 0,
		"", "NOCC_LOG_VERBOSITY")
	disableObjCache := common.CmdEnvBool("Disable obj cache on remote: .o will be compiled always and won't be stored.", false,
		"", "NOCC_DISABLE_OBJ_CACHE")
	disableOwnIncludes := common.CmdEnvBool("Disable own includes parser: use a C++ preprocessor instead.\nIt's much slower, but 100% works.\nBy default, nocc traverses #include-s recursively using its own built-in parser.", false,
		"", "NOCC_DISABLE_OWN_INCLUDES")
	localCxxQueueSize := common.CmdEnvInt("Amount of parallel processes when remotes aren't available and cxx is launched locally.\nBy default, it's a number of CPUs on the current machine.", int64(runtime.NumCPU()),
		"", "NOCC_LOCAL_CXX_QUEUE_SIZE")
	forceInterruptTimeout := common.CmdEnvInt("Timeout after how long the daemon will force a connection termination (The value is specified in minutes). By default, it's 8 minutes.", 8,
		"", "NOCC_FORCE_INTERRUPT_TIMEOUT")

	common.ParseCmdFlagsCombiningWithEnv()

	var remoteNoccHosts []string
	if *noccServers != "" {
		remoteNoccHosts = parseNoccServersEnv(*noccServers)
	} else if *noccServersFilename != "" {
		remoteNoccHosts = readNoccServersFile(*noccServersFilename)
	}

	if *showVersionAndExit || *showVersionAndExitShort {
		fmt.Println(common.GetVersion())
		os.Exit(0)
	}

	if *checkServersAndExit {
		if len(os.Args) == 3 { // nocc -check-servers {remoteHostPort}
			remoteNoccHosts = []string{os.Args[2]}
		}
		if len(remoteNoccHosts) == 0 {
			failedStart("no remote hosts set; you should set NOCC_SERVERS or NOCC_SERVERS_FILENAME")
		}
		client.RequestRemoteStatus(remoteNoccHosts)
		os.Exit(0)
	}

	if *dumpServerLogsAndExit {
		if len(os.Args) == 3 { // nocc -dump-server-logs {remoteHostPort}
			remoteNoccHosts = []string{os.Args[2]}
		}
		if len(remoteNoccHosts) == 0 {
			failedStart("no remote hosts set; you should set NOCC_SERVERS or NOCC_SERVERS_FILENAME")
		}
		client.RequestRemoteDumpLogs(remoteNoccHosts, "/tmp/nocc-dump-logs")
		os.Exit(0)
	}

	if *dropServerCachesAndExit {
		if len(remoteNoccHosts) == 0 {
			failedStart("no remote hosts set; you should set NOCC_SERVERS or NOCC_SERVERS_FILENAME")
		}
		client.RequestDropAllCaches(remoteNoccHosts)
		os.Exit(0)
	}

	// `nocc-daemon start {cxxName}`
	// on init fail, we should print an error to stdout (a parent process is listening to stdout pipe)
	// on init success, we should print '1' to stdout
	if len(os.Args) == 2 && os.Args[1] == "start" {
		if err := client.MakeLoggerClient(*logFileName, *logVerbosity, *logFileName != "stderr"); err != nil {
			failedStartDaemon(err)
		}

		daemon, err := client.MakeDaemon(remoteNoccHosts, *disableObjCache, *disableOwnIncludes, *localCxxQueueSize, *forceInterruptTimeout)
		if err != nil {
			failedStartDaemon(err)
		}
		err = daemon.StartListeningUnixSocket("/tmp/nocc.sock")
		if err != nil {
			failedStartDaemon(err)
		}
		fmt.Printf("1\000\n")

		daemon.ServeUntilNobodyAlive()
		return
	}

	// if we reached this line, then `nocc-daemon g++ ...` was launched directly (not a C++ `nocc` wrapper)
	// it's mostly for dev purposes: we execute the query like we are inside a daemon, then die.

	if err := client.MakeLoggerClient(*logFileName, *logVerbosity, false); err != nil {
		failedStart(err)
	}

	if len(os.Args) < 3 {
		failedStart("invalid usage: compiler line expected; example: 'nocc g++ main.cpp -o main.o'")
	}

	if len(remoteNoccHosts) == 0 {
		failedStart("no remote hosts set; you should set NOCC_SERVERS or NOCC_SERVERS_FILENAME")
	}

	exitCode, stdout, stderr := client.EmulateDaemonInsideThisProcessForDev(remoteNoccHosts, os.Args[1:], *disableOwnIncludes, 1)
	_, _ = os.Stdout.Write(stdout)
	_, _ = os.Stderr.Write(stderr)
	os.Exit(exitCode)
}

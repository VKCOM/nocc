package tests

import (
	"os/exec"
	"strings"
	"time"

	"github.com/VKCOM/nocc/internal/client"
)

func createClientAndEmulateDaemonForTesting(cmdLineStr string) (exitCode int, stdout []byte, stderr []byte, err error) {
	var cmdLine = strings.Split(cmdLineStr, " ")
	var remoteNoccHosts = []string{"127.0.0.1:43210"}
	var logFile = ""
	var logVerbosity = int64(-1)

	if err = client.MakeLoggerClient(logFile, logVerbosity, false); err != nil {
		return
	}

	exitCode, stdout, stderr = client.EmulateDaemonInsideThisProcessForDev(remoteNoccHosts, cmdLine, false, 0, 8*time.Minute)
	time.Sleep(100 * time.Millisecond) // for all goroutines to finish
	return
}

func runDaemonInBackgroundForTesting() error {
	cmd := exec.Command("../bin/nocc-daemon", "start")
	cmd.Env = []string{
		"NOCC_SERVERS=127.0.0.1:43210",
	}
	return cmd.Start()
}

func runCmdLocallyForTesting(cmdLineStr string) (exitCode int, output []byte, err error) {
	cmdLine := strings.Split(cmdLineStr, " ")

	cmd := exec.Command(cmdLine[0], cmdLine[1:]...)

	output, err = cmd.CombinedOutput()
	exitCode = cmd.ProcessState.ExitCode()
	return
}

package client

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
)

// LocalCxxLaunch describes an invocation when it's executed locally, not remotely.
// When some remotes are not available, files that were calculated to be compiled on that remotes,
// fall back to local compilation.
// Note, that local compilation is performed within a daemon instead of passing it to C++ wrappers.
// This is done in order to maintain a single queue.
// (`nocc` is typically launched with a very huge number of concurrent processes, and if network is broken,
//  this queue makes a huge bunch of `nocc` invocations to be throttled to a limited number of local cxx processes).
type LocalCxxLaunch struct {
	cmdLine []string
	cwd     string
}

func (localCxx *LocalCxxLaunch) RunCxxLocally() (exitCode int, stdout []byte, stderr []byte) {
	logClient.Info(0, "compile locally", localCxx.cmdLine)

	cxxCommand := exec.Command(localCxx.cmdLine[0], localCxx.cmdLine[1:]...)
	cxxCommand.Dir = localCxx.cwd
	var cxxStdout, cxxStderr bytes.Buffer
	cxxCommand.Stdout = &cxxStdout
	cxxCommand.Stderr = &cxxStderr
	err := cxxCommand.Run()

	exitCode = cxxCommand.ProcessState.ExitCode()
	stdout = cxxStdout.Bytes()
	stderr = cxxStderr.Bytes()
	if len(stderr) == 0 && err != nil {
		stderr = []byte(fmt.Sprintln(err))
	}
	return
}

// EmulateDaemonInsideThisProcessForDev is for dev purposes:
// for development, I use `nocc-daemon g++ ...` from GoLand directly (without a C++ `nocc` wrapper).
func EmulateDaemonInsideThisProcessForDev(remoteNoccHosts []string, cmdLine []string, disableOwnIncludes bool, localCxxQueueSize int) (exitCode int, stdout []byte, stderr []byte) {
	daemon, err := MakeDaemon(remoteNoccHosts, false, disableOwnIncludes, int64(localCxxQueueSize), 3000)
	if err != nil {
		panic(err)
	}
	defer daemon.QuitDaemonGracefully("done")
	go daemon.PeriodicallyInterruptHangedInvocations()

	cwd, _ := os.Getwd()
	request := DaemonSockRequest{cwd, cmdLine}
	response := daemon.HandleInvocation(request)

	return response.ExitCode, response.Stdout, response.Stderr
}

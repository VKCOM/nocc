package server

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

type CxxLauncher struct {
	chanToCompile chan *Session
}

func MakeCxxLauncher() (*CxxLauncher, error) {
	return &CxxLauncher{
		chanToCompile: make(chan *Session, 50),
	}, nil
}

func (cxxLauncher *CxxLauncher) EnterInfiniteLoopToCompile(noccServer *NoccServer) {
	for session := range cxxLauncher.chanToCompile {
		go cxxLauncher.launchServerCxxForCpp(session, noccServer)
	}
}

func (cxxLauncher *CxxLauncher) launchServerCxxForCpp(session *Session, noccServer *NoccServer) {
	cxxCommand := exec.Command(session.cxxName, session.cxxCmdLine...)
	cxxCommand.Dir = session.client.workingDir
	var cxxStdout, cxxStderr bytes.Buffer
	cxxCommand.Stderr = &cxxStderr
	cxxCommand.Stdout = &cxxStdout

	logServer.Info(1, "launch cxx", "sessionID", session.sessionID, "clientID", session.client.clientID)
	atomic.AddInt64(&noccServer.Stats.cxxCalls, 1)
	start := time.Now()
	err := cxxCommand.Run()
	session.cxxDuration = int32(time.Since(start).Milliseconds())
	atomic.AddInt64(&noccServer.Stats.cxxTotalDurationMs, int64(session.cxxDuration))

	session.cxxExitCode = int32(cxxCommand.ProcessState.ExitCode())
	session.cxxStdout = cxxLauncher.patchStdoutDropServerPaths(session.client, cxxStdout.Bytes())
	session.cxxStderr = cxxLauncher.patchStdoutDropServerPaths(session.client, cxxStderr.Bytes())
	if len(session.cxxStderr) == 0 && err != nil {
		session.cxxStderr = []byte(fmt.Sprintln(err))
	}

	if session.cxxExitCode != 0 {
		atomic.AddInt64(&noccServer.Stats.cxxNonZeroExitCode, 1)
		logServer.Error("the C++ compiler exited with code", session.cxxExitCode, "sessionID", session.sessionID, session.cppInFile, "\ncxxCmdLine:", session.cxxName, session.cxxCmdLine, "\ncxxStdout:", strings.TrimSpace(string(session.cxxStdout)), "\ncxxStderr:", strings.TrimSpace(string(session.cxxStderr)))
	}

	// save to obj cache (to be safe, only if cxx output is empty)
	if !session.objCacheKey.IsEmpty() {
		if session.cxxExitCode == 0 && len(session.cxxStdout) == 0 && len(session.cxxStderr) == 0 {
			if stat, err := os.Stat(session.objOutFile); err == nil {
				_ = noccServer.ObjFileCache.SaveFileToCache(session.objOutFile, session.objCacheKey, stat.Size())
			}
		}
	}

	session.PushToClientReadyChannel()
}

func (cxxLauncher *CxxLauncher) launchServerCxxForPch(cxxName string, cxxCmdLine []string, rootDir string, noccServer *NoccServer) error {
	cxxCommand := exec.Command(cxxName, cxxCmdLine...)
	cxxCommand.Dir = rootDir
	var cxxStdout, cxxStderr bytes.Buffer
	cxxCommand.Stderr = &cxxStderr
	cxxCommand.Stdout = &cxxStdout

	logServer.Info(1, "launch cxx for pch compilation", "rootDir", rootDir)
	atomic.AddInt64(&noccServer.Stats.pchCompilations, 1)
	_ = cxxCommand.Run()

	cxxExitCode := cxxCommand.ProcessState.ExitCode()

	if cxxExitCode != 0 {
		atomic.AddInt64(&noccServer.Stats.pchCompilationsFailed, 1)
		logServer.Error("the C++ compiler exited with code pch", cxxExitCode, "\ncmdLine:", cxxName, cxxCmdLine, "\ncxxStdout:", strings.TrimSpace(cxxStdout.String()), "\ncxxStderr:", strings.TrimSpace(cxxStderr.String()))
		return fmt.Errorf("could not compile pch: the C++ compiler exited with code %d\n%s", cxxExitCode, cxxStdout.String()+cxxStderr.String())
	}

	return nil
}

// patchStdoutDropServerPaths replaces /tmp/nocc-server/clients/clientID/path/to/file.cpp with /path/to/file.cpp.
// It's very handy to send back stdout/stderr without server paths.
func (cxxLauncher *CxxLauncher) patchStdoutDropServerPaths(client *Client, stdout []byte) []byte {
	if len(stdout) == 0 {
		return stdout
	}

	return bytes.ReplaceAll(stdout, []byte(client.workingDir), []byte{})
}

package server

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

type CxxLauncher struct {
	serverCxxThrottle chan struct{}

	nSessionsReadyButWaiting int64
	nSessionsNowCompiling    int64

	totalCalls           int64
	totalDurationMs      int64
	more10secCount       int64
	more30secCount       int64
	nonZeroExitCodeCount int64
}

func MakeCxxLauncher(maxParallelCxxProcesses int64) (*CxxLauncher, error) {
	if maxParallelCxxProcesses <= 0 {
		return nil, fmt.Errorf("invalid maxParallelCxxProcesses %d", maxParallelCxxProcesses)
	}

	return &CxxLauncher{
		serverCxxThrottle: make(chan struct{}, maxParallelCxxProcesses),
	}, nil
}

// LaunchCxxWhenPossible launches the C++ compiler on a server managing a waiting queue.
// The purpose of a waiting queue is not to over-utilize server resources at peak times.
// Currently, amount of max parallel C++ processes is an option provided at start up
// (it other words, it's not dynamic, nocc-server does not try to analyze CPU/memory).
func (cxxLauncher *CxxLauncher) LaunchCxxWhenPossible(noccServer *NoccServer, session *Session) {
	atomic.AddInt64(&cxxLauncher.nSessionsReadyButWaiting, 1)
	cxxLauncher.serverCxxThrottle <- struct{}{} // blocking

	atomic.AddInt64(&cxxLauncher.nSessionsReadyButWaiting, -1)
	curParallelCount := atomic.AddInt64(&cxxLauncher.nSessionsNowCompiling, 1)

	logServer.Info(1, "launch cxx #", curParallelCount, "sessionID", session.sessionID, "clientID", session.client.clientID, session.cppInFile)
	cxxLauncher.launchServerCxxForCpp(session, noccServer) // blocking until cxx ends

	atomic.AddInt64(&cxxLauncher.nSessionsNowCompiling, -1)
	atomic.AddInt64(&cxxLauncher.totalCalls, 1)
	atomic.AddInt64(&cxxLauncher.totalDurationMs, int64(session.cxxDuration))

	if session.cxxExitCode != 0 {
		atomic.AddInt64(&cxxLauncher.nonZeroExitCodeCount, 1)
	} else if session.cxxDuration > 30000 {
		atomic.AddInt64(&cxxLauncher.more30secCount, 1)
	} else if session.cxxDuration > 10000 {
		atomic.AddInt64(&cxxLauncher.more10secCount, 1)
	}

	<-cxxLauncher.serverCxxThrottle
	session.PushToClientReadyChannel()
}

func (cxxLauncher *CxxLauncher) GetNowCompilingSessionsCount() int64 {
	return atomic.LoadInt64(&cxxLauncher.nSessionsNowCompiling)
}

func (cxxLauncher *CxxLauncher) GetWaitingInQueueSessionsCount() int64 {
	return atomic.LoadInt64(&cxxLauncher.nSessionsReadyButWaiting)
}

func (cxxLauncher *CxxLauncher) GetTotalCxxCallsCount() int64 {
	return atomic.LoadInt64(&cxxLauncher.totalCalls)
}

func (cxxLauncher *CxxLauncher) GetTotalCxxDurationMilliseconds() int64 {
	return atomic.LoadInt64(&cxxLauncher.totalDurationMs)
}

func (cxxLauncher *CxxLauncher) GetMore10secCount() int64 {
	return atomic.LoadInt64(&cxxLauncher.more10secCount)
}

func (cxxLauncher *CxxLauncher) GetMore30secCount() int64 {
	return atomic.LoadInt64(&cxxLauncher.more30secCount)
}

func (cxxLauncher *CxxLauncher) GetNonZeroExitCodeCount() int64 {
	return atomic.LoadInt64(&cxxLauncher.nonZeroExitCodeCount)
}

func (cxxLauncher *CxxLauncher) launchServerCxxForCpp(session *Session, noccServer *NoccServer) {
	cxxCommand := exec.Command(session.cxxName, session.cxxCmdLine...)
	cxxCommand.Dir = session.cxxCwd
	var cxxStdout, cxxStderr bytes.Buffer
	cxxCommand.Stderr = &cxxStderr
	cxxCommand.Stdout = &cxxStdout

	start := time.Now()
	err := cxxCommand.Run()

	session.cxxDuration = int32(time.Since(start).Milliseconds())
	session.cxxExitCode = int32(cxxCommand.ProcessState.ExitCode())
	session.cxxStdout = cxxStdout.Bytes()
	session.cxxStderr = cxxStderr.Bytes()
	if len(session.cxxStderr) == 0 && err != nil {
		session.cxxStderr = []byte(fmt.Sprintln(err))
	}

	if session.cxxExitCode != 0 {
		logServer.Error("the C++ compiler exited with code", session.cxxExitCode, "sessionID", session.sessionID, session.cppInFile, "\ncxxCwd:", session.cxxCwd, "\ncxxCmdLine:", session.cxxName, session.cxxCmdLine, "\ncxxStdout:", strings.TrimSpace(string(session.cxxStdout)), "\ncxxStderr:", strings.TrimSpace(string(session.cxxStderr)))
	} else if session.cxxDuration > 30000 {
		logServer.Info(0, "compiled very heavy file", "sessionID", session.sessionID, "cxxDuration", session.cxxDuration, session.cppInFile)
	}

	// save to obj cache (to be safe, only if cxx output is empty)
	if !session.objCacheKey.IsEmpty() {
		if session.cxxExitCode == 0 && len(session.cxxStdout) == 0 && len(session.cxxStderr) == 0 {
			if stat, err := os.Stat(session.objOutFile); err == nil {
				_ = noccServer.ObjFileCache.SaveFileToCache(session.objOutFile, path.Base(session.cppInFile)+".o", session.objCacheKey, stat.Size())
			}
		}
	}

	session.cxxStdout = cxxLauncher.patchStdoutDropServerPaths(session.client, session.cxxStdout)
	session.cxxStderr = cxxLauncher.patchStdoutDropServerPaths(session.client, session.cxxStderr)
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

// patchStdoutDropServerPaths replaces /tmp/nocc/cpp/clients/clientID/path/to/file.cpp with /path/to/file.cpp.
// It's very handy to send back stdout/stderr without server paths.
func (cxxLauncher *CxxLauncher) patchStdoutDropServerPaths(client *Client, stdout []byte) []byte {
	if len(stdout) == 0 {
		return stdout
	}

	return bytes.ReplaceAll(stdout, []byte(client.workingDir), []byte{})
}

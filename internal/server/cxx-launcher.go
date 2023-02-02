package server

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/VKCOM/nocc/internal/common"
)

type CxxLauncher struct {
	chanToCompile chan *Session
}

func MakeCxxLauncher() (*CxxLauncher, error) {
	return &CxxLauncher{
		chanToCompile: make(chan *Session, runtime.NumCPU()),
	}, nil
}

func (cxxLauncher *CxxLauncher) EnterInfiniteLoopToCompile(noccServer *NoccServer) {
	for session := range cxxLauncher.chanToCompile {
		go cxxLauncher.launchServerCxxForCpp(session, noccServer)
	}
}

// symlinkSystemFilesToClientWorkingDir deals with the following situation.
// If session.cppInFile doesn't exist on a server, it means the following: an input file is in /usr,
// which is equal on a server and on a client, and it just hasn't been uploaded.
// In that case, cppInFile = /tmp/.../usr/..., clientFileName = /usr/..., cxxArgs contain -I /tmp/.../usr/...
// To make compilation succeed in workingDir, symlink all dependencies from a system folder into workingDir.
// Note, that we use symlinks, because we have no permissions on hardlinks for that locations.
func (cxxLauncher *CxxLauncher) symlinkSystemFilesToClientWorkingDir(session *Session, systemHeaders *SystemHeadersCache) {
	for _, file := range session.files {
		// for system headers, file.serverFileName is /usr/..., not /tmp/...
		if systemHeaders.IsSystemHeader(file.serverFileName, file.fileSize, file.fileSHA256) {
			clientFileName := file.serverFileName
			serverFileName := session.client.MapClientFileNameToServerAbs(clientFileName)

			logServer.Info(2, "symlink", clientFileName, "to", serverFileName)
			_ = common.MkdirForFile(serverFileName)
			if err := os.Symlink(clientFileName, serverFileName); err != nil && !os.IsExist(err) {
				logServer.Error("symlink from", clientFileName, "failed", err)
			}
		}
	}
}

func (cxxLauncher *CxxLauncher) launchServerCxxForCpp(session *Session, noccServer *NoccServer) {
	// read a comment above symlinkSystemFilesToClientWorkingDir()
	if _, err := os.Stat(session.cppInFile); os.IsNotExist(err) {
		logServer.Info(1, "not found cpp", session.cppInFile, ", symlink system files to", session.client.workingDir)
		cxxLauncher.symlinkSystemFilesToClientWorkingDir(session, noccServer.SystemHeaders)
	}

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
	session.cxxExitCode = int32(cxxCommand.ProcessState.ExitCode())
	session.cxxStdout = cxxStdout.Bytes()
	session.cxxStderr = cxxStderr.Bytes()
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

	atomic.AddInt64(&noccServer.Stats.cxxTotalDurationMs, int64(session.cxxDuration))
	session.cxxStdout = cxxLauncher.patchStdoutDropServerPaths(session.client, session.cxxStdout)
	session.cxxStderr = cxxLauncher.patchStdoutDropServerPaths(session.client, session.cxxStderr)
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

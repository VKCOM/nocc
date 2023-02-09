package server

import (
	"sync/atomic"

	"github.com/VKCOM/nocc/internal/common"
)

// Session is created when a client requests to compile a .cpp file.
// It's a server representation of client.Invocation.
// A lifetime of one Session is the following:
// 1) a session is created, provided a .cpp file and all .h/.nocc-pch/etc. dependencies
// 2) files that don't exist on this server are uploaded by the client
// 3) the C++ compiler (cxx) is launched
// 4) the client downloads .o
// 5) the session is closed automatically
// Steps 2-5 can be skipped if a compiled .o already exists in ObjFileCache.
type Session struct {
	sessionID uint32

	cppInFile  string // as-is from a client cmd line (relative to cxxCwd on a server-side)
	objOutFile string // inside /tmp/nocc/obj/cxx-out, or directly in /tmp/nocc/obj/obj-cache if taken from cache
	cxxCwd     string // cwd for the C++ compiler on a server-side (= client.workingDir + clientCwd)
	cxxName    string // g++ / clang / etc.
	cxxCmdLine []string

	client *Client
	files  []*fileInClientDir

	objCacheKey        common.SHA256
	objCacheExists     bool
	compilationStarted int32

	cxxExitCode int32
	cxxStdout   []byte
	cxxStderr   []byte
	cxxDuration int32
}

// PrepareServerCxxCmdLine prepares a command line for cxx invocation.
// Notably, options like -Wall and -fpch-preprocess are pushed as is,
// but include dirs like /home/alice/headers need to be remapped to point to server dir.
func (session *Session) PrepareServerCxxCmdLine(noccServer *NoccServer, clientCwd string, cxxArgs []string, cxxIDirs []string) {
	session.objOutFile = noccServer.ObjFileCache.GenerateObjOutFileName(session)

	// old clients that don't send this field (they send abs cppInFile)
	// todo delete later, after upgrading all clients
	if clientCwd == "" {
		session.cppInFile = session.client.MapClientFileNameToServerAbs(session.cppInFile)
		session.cxxCwd = session.client.workingDir
	} else {
		// session.cppInFile is relative to clientCwd: as-is from a client command line
		session.cxxCwd = session.client.MapClientFileNameToServerAbs(clientCwd)
	}

	cxxCmdLine := make([]string, 0, len(cxxIDirs)+len(cxxArgs)+3)

	// loop through -I {dir} / -include {file} / etc. (format is guaranteed), converting client {dir} to server path
	for i := 0; i < len(cxxIDirs); i += 2 {
		arg := cxxIDirs[i]
		serverIdir := session.client.MapClientFileNameToServerAbs(cxxIDirs[i+1])
		cxxCmdLine = append(cxxCmdLine, arg, serverIdir)
	}
	// append -Wall and other cxx args
	cxxCmdLine = append(cxxCmdLine, cxxArgs...)
	// build final string
	session.cxxCmdLine = append(cxxCmdLine, "-o", session.objOutFile, session.cppInFile)
}

// StartCompilingObjIfPossible executes cxx if all dependent files (.cpp/.h/.nocc-pch/etc.) are ready.
// They have either been uploaded by the client or already taken from src cache.
// Note, that it's called for sessions that don't exist in obj cache.
func (session *Session) StartCompilingObjIfPossible(noccServer *NoccServer) {
	for _, file := range session.files {
		if file.state != fsFileStateUploaded {
			return
		}
	}

	if atomic.SwapInt32(&session.compilationStarted, 1) == 0 {
		go noccServer.CxxLauncher.LaunchCxxWhenPossible(noccServer, session)
	}
}

func (session *Session) PushToClientReadyChannel() {
	// a client could have disconnected while cxx was working, then chanDisconnected is closed
	select {
	case <-session.client.chanDisconnected:
	case session.client.chanReadySessions <- session:
		// note, that if this chan is full, this 'case' (and this function call) is blocking
	}
}

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

	cppInFile  string
	objOutFile string
	cxxName    string
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

// StartCompilingObjIfPossible executes cxx if all dependent files (.cpp/.h/.nocc-pch/etc.) are ready.
// They have either been uploaded by the client or already taken from src cache.
func (session *Session) StartCompilingObjIfPossible(noccServer *NoccServer) {
	// optimistic path: if .o file exists in cache, files aren't needed to (and aren't requested to) be uploaded
	if session.objCacheExists { // avoid calling ExistsInCache (when false, it's launched on every file upload)
		if atomic.SwapInt32(&session.compilationStarted, 1) == 0 {
			logServer.Info(2, "get obj from cache", "sessionID", session.sessionID, session.objOutFile)
			if !noccServer.ObjFileCache.CreateHardLinkFromCache(session.objOutFile, session.objCacheKey) {
				logServer.Error("could not create hard link from obj cache", "sessionID", session.sessionID)
			}
			session.PushToClientReadyChannel()
		}
		return
	}

	for _, file := range session.files {
		if file.state != fsFileStateUploaded {
			return
		}
	}

	if atomic.SwapInt32(&session.compilationStarted, 1) == 0 {
		noccServer.CxxLauncher.chanToCompile <- session
	}
}

func (session *Session) PushToClientReadyChannel() {
	// a client could have disconnected while cxx was working, then chanDisconnected is closed
	select {
	case <-session.client.chanDisconnected:
	case session.client.chanReadySessions <- session:
	}
}

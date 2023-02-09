package server

import (
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/VKCOM/nocc/pb"
)

const (
	fsFileStateJustCreated = iota
	fsFileStateUploading
	fsFileStateUploadError
	fsFileStateUploaded
)

// fileInClientDir describes a file on a server file system inside a client working dir.
// When multiple client nocc processes are launched (the same clientID), they simultaneously start uploading files,
// which are saved into a folder with relative paths equal to absolute client paths.
//
// For example, a client uploads 3 files: /home/alice/1.cpp, /home/alice/1.h, /usr/include/math.h.
// They are saved to /tmp/nocc/cpp/clients/{clientID}/home/alice/1.cpp and so on.
// (if math.h is equal to a server system include /usr/include/math.h, it isn't requested to be uploaded).
//
// fileInClientDir also represents files in the process of uploading, before actually saved to a disk (state field).
//
// Note, that files inside a client working dir are not _all_ files from a client: only files uploaded to current shard.
// Having 3 nocc-server hosts, a client balances between them based on a .cpp basename.
type fileInClientDir struct {
	fileSize   int64
	fileSHA256 common.SHA256

	state           int // fsFileState*
	uploadStartTime time.Time

	serverFileName string // abs path, see Client.MapClientFileNameToServerAbs
}

// Client represents a client machine that has set up a connection to server.
// When a nocc process starts on a client machine, it generates a stable but unique clientID.
// So, multiple nocc process starting at the same machine simultaneously are one client, actually.
// Every client as a workingDir, where all files uploaded from that client are saved to.
type Client struct {
	clientID   string
	workingDir string    // /tmp/nocc/cpp/clients/{clientID}
	lastSeen   time.Time // to detect when a client becomes inactive

	mu       sync.RWMutex
	sessions map[uint32]*Session
	files    map[string]*fileInClientDir // from clientFileName to a server file
	dirs     map[string]bool             // not to call MkdirAll for every file, key is path.Dir(serverFileName)

	chanDisconnected  chan struct{}
	chanReadySessions chan *Session
	disableObjCache   bool
}

func (client *Client) makeNewFile(clientFileName string, fileSize int64, fileSHA256 common.SHA256) *fileInClientDir {
	return &fileInClientDir{
		fileSize:        fileSize,
		fileSHA256:      fileSHA256,
		serverFileName:  client.MapClientFileNameToServerAbs(clientFileName),
		state:           fsFileStateJustCreated,
		uploadStartTime: time.Now(),
	}
}

// MapClientFileNameToServerAbs converts a client file name to an absolute path on server.
// For example, /proj/1.cpp maps to /tmp/nocc/cpp/clients/{clientID}/proj/1.cpp.
// Note, that system files like /usr/local/include are required to be equal on both sides.
// (if not, a server session will fail to start, and a client will fall back to local compilation)
func (client *Client) MapClientFileNameToServerAbs(clientFileName string) string {
	if clientFileName[0] == '/' {
		if IsSystemHeaderPath(clientFileName) {
			return clientFileName
		}
		return client.workingDir + clientFileName
	}
	return path.Join(client.workingDir, clientFileName)
}

// MapServerAbsToClientFileName converts an absolute path on server relatively to the client working dir.
// For example, /tmp/nocc/cpp/clients/{clientID}/proj/1.cpp maps to /proj/1.cpp.
// If serverFileName is /usr/local/include, it's left as is.
func (client *Client) MapServerAbsToClientFileName(serverFileName string) string {
	return strings.TrimPrefix(serverFileName, client.workingDir)
}

func (client *Client) CreateNewSession(in *pb.StartCompilationSessionRequest) (*Session, error) {
	newSession := &Session{
		sessionID: in.SessionID,
		files:     make([]*fileInClientDir, len(in.RequiredFiles)),
		cxxName:   in.CxxName,
		cppInFile: in.CppInFile, // as specified in a client cmd line invocation (relative to in.Cwd or abs on a client file system)
		client:    client,
		// objOutFile is filled only in cxx is required to be called, see Session.PrepareServerCxxCmdLine()
	}

	for index, meta := range in.RequiredFiles {
		fileSHA256 := common.SHA256{B0_7: meta.SHA256_B0_7, B8_15: meta.SHA256_B8_15, B16_23: meta.SHA256_B16_23, B24_31: meta.SHA256_B24_31}
		file, err := client.StartUsingFileInSession(meta.ClientFileName, meta.FileSize, fileSHA256)
		newSession.files[index] = file
		// the only reason why a session can't be created is a dependency conflict:
		// previously, a client reported that clientFileName has sha256=v1, and now it sends sha256=v2
		if err != nil {
			return nil, err
		}
	}

	// note, that we don't add newSession to client.sessions: it's just created, not registered
	// (so, it won't be enumerated in a loop inside GetSessionsNotStartedCompilation until registered)

	return newSession, nil
}

func (client *Client) RegisterCreatedSession(session *Session) {
	client.mu.Lock()
	client.sessions[session.sessionID] = session
	client.mu.Unlock()
}

func (client *Client) CloseSession(session *Session) {
	client.mu.Lock()
	delete(client.sessions, session.sessionID)
	client.mu.Unlock()

	if !session.objCacheExists { // delete /tmp/nocc/obj/cxx-out/this.o (already hard linked to obj cache)
		_ = os.Remove(session.objOutFile)
	}
	session.files = nil
}

func (client *Client) GetSession(sessionID uint32) *Session {
	client.mu.RLock()
	session := client.sessions[sessionID]
	client.mu.RUnlock()

	return session
}

func (client *Client) GetActiveSessionsCount() int {
	client.mu.RLock()
	count := len(client.sessions)
	client.mu.RUnlock()

	return count
}

func (client *Client) GetSessionsNotStartedCompilation() []*Session {
	sessions := make([]*Session, 0)
	client.mu.RLock()
	for _, session := range client.sessions { // loop over registered sessions
		if atomic.LoadInt32(&session.compilationStarted) == 0 {
			sessions = append(sessions, session)
		}
	}
	client.mu.RUnlock()
	return sessions
}

// StartUsingFileInSession is called on a session creation for a .cpp file and all dependencies.
// If it's the first time we see clientFileName, it's created (we start waiting for it to be uploaded).
// If it already exists, compare client sha256 with what we have (if equal, don't need to upload this file again).
//
// The only reason why we can return an error here is a dependency conflict:
// previously, a client reported that clientFileName has sha256=v1, and now it sends sha256=v2.
func (client *Client) StartUsingFileInSession(clientFileName string, fileSize int64, fileSHA256 common.SHA256) (*fileInClientDir, error) {
	client.mu.RLock()
	file := client.files[clientFileName]
	client.mu.RUnlock()

	if file == nil {
		client.mu.Lock()
		file = client.files[clientFileName]
		if file != nil {
			client.mu.Unlock()
			return file, nil
		}
		newFile := client.makeNewFile(clientFileName, fileSize, fileSHA256)
		client.files[clientFileName] = newFile
		client.mu.Unlock()
		return newFile, nil
	}

	if file.fileSHA256 != fileSHA256 {
		return nil, fmt.Errorf("file %s was already uploaded, but now got another sha256 from client", clientFileName)
	}

	return file, nil
}

// MkdirAllForSession ensures that all directories for saving files from session exist
// (they mirror client directory structure in client.workingDir).
// Instead of calling os.MkdirAll for every uploaded or hard linked file, they are created in advance.
// Moreover, we need to call os.MkdirAll only once for all files within it (when it appears first time).
// After this call, every /home/file.h can be saved into /tmp/.../{clientID}/home/file.h.
func (client *Client) MkdirAllForSession(session *Session) {
	dirsToCreate := make([]string, 0)

	client.mu.RLock()
	for _, file := range session.files {
		lastSlash := len(file.serverFileName) - 1
		for file.serverFileName[lastSlash] != '/' {
			lastSlash--
		}
		dir := file.serverFileName[0:lastSlash]
		if exists := client.dirs[dir]; !exists {
			// session.files (includes order) is often partially sorted, so add fewer duplicates
			if len(dirsToCreate) == 0 || dirsToCreate[len(dirsToCreate)-1] != dir {
				dirsToCreate = append(dirsToCreate, dir)
			}
		}
	}
	if exists := client.dirs[session.cxxCwd]; !exists {
		dirsToCreate = append(dirsToCreate, session.cxxCwd)
	}
	client.mu.RUnlock()

	if len(dirsToCreate) == 0 {
		return
	}

	for _, dir := range dirsToCreate {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			logServer.Error("can't create dir", dir, err)
		}
	}

	client.mu.Lock()
	for _, dir := range dirsToCreate {
		client.dirs[dir] = true
	}
	client.mu.Unlock()
}

// IsFileUploadHanged checks whether a file upload lasts too long, and a file should be re-requested.
// A timeout depends on file size: for instance, .nocc-pch files are big, we'll wait for them for a long time
// (especially when nocc client uploads it to all servers, the network on a client machine suffers).
func (client *Client) IsFileUploadHanged(fileWithStateUploading *fileInClientDir) bool {
	passedSec := time.Since(fileWithStateUploading.uploadStartTime).Seconds()

	if fileWithStateUploading.fileSize > 5*1024*1024 {
		return passedSec > 60
	}
	return passedSec > 15
}

func (client *Client) RemoveWorkingDir() {
	workingDirRenamed := fmt.Sprintf("%s.old.%d", client.workingDir, time.Now().Unix())

	client.mu.Lock()
	_ = os.Rename(client.workingDir, workingDirRenamed)
	client.files = make(map[string]*fileInClientDir)
	client.mu.Unlock()

	go func() {
		if err := os.RemoveAll(workingDirRenamed); err != nil {
			logServer.Error("could not remove client working dir", "clientID", client.clientID, workingDirRenamed, err)
		}
	}()
}

func (client *Client) FilesCount() int64 {
	client.mu.RLock()
	filesCount := len(client.files)
	client.mu.RUnlock()
	return int64(filesCount)
}

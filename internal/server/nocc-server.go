package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	// "syscall"
	"time"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/VKCOM/nocc/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// NoccServer stores all server's state and serves grpc requests.
// Remember, that in practice, the nocc-server process is started on different physical nodes (shards),
// and nocc clients balance between them based on .cpp basename.
type NoccServer struct {
	pb.UnimplementedCompilationServiceServer
	GRPCServer *grpc.Server

	StartTime time.Time

	Cron  *Cron
	Stats *Statsd

	ActiveClients  *ClientsStorage
	CxxLauncher    *CxxLauncher
	PchCompilation *PchCompilation

	SystemHeaders *SystemHeadersCache
	SrcFileCache  *SrcFileCache
	ObjFileCache  *ObjFileCache
}

func launchCxxOnServerOnReadySessions(noccServer *NoccServer, client *Client) {
	for _, session := range client.GetSessionsNotStartedCompilation() {
		session.StartCompilingObjIfPossible(noccServer)
	}
}

// StartGRPCListening is an entrypoint called from main() of nocc-server.
// It either returns an error or starts processing grpc requests and never ends.
func (s *NoccServer) StartGRPCListening(listenAddr string) (net.Listener, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	go s.Cron.StartCron()

	logServer.Info(0, "nocc-server started")

	// var rLimit syscall.Rlimit
	// _ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	// logServer.Info(0, "env:", "listenAddr", listenAddr, "; ulimit -n", rLimit.Cur, "; num cpu", runtime.NumCPU(), "; version", common.GetVersion())
	logServer.Info(0, "env:", "listenAddr", listenAddr, "; num cpu", runtime.NumCPU(), "; version", common.GetVersion())

	return listener, s.GRPCServer.Serve(listener)
}

// QuitServerGracefully closes all active clients and stops accepting new connections.
// After it, StartGRPCListening returns, and main() continues.
func (s *NoccServer) QuitServerGracefully() {
	logServer.Info(0, "graceful stop...")

	s.Stats.Close()
	s.Cron.StopCron()
	s.ActiveClients.StopAllClients()
	s.GRPCServer.GracefulStop()
}

// StartClient is a grpc handler.
// When a nocc-daemon starts, it sends this query — before starting any session.
// So, one client == one running nocc-daemon. All clients have unique clientID.
// When a nocc-daemon exits, it sends StopClient (or when it dies unexpectedly, a client is deleted after timeout).
func (s *NoccServer) StartClient(_ context.Context, in *pb.StartClientRequest) (*pb.StartClientReply, error) {
	client, err := s.ActiveClients.OnClientConnected(in.ClientID, in.DisableObjCache)
	if err != nil {
		return nil, err
	}

	logServer.Info(0, "new client", "clientID", client.clientID, "version", in.ClientVersion, "; nClients", s.ActiveClients.ActiveCount())

	if in.AllRemotesDelim != "" && s.ActiveClients.IsRemotesListSeenTheFirstTime(in.AllRemotesDelim, client.clientID) {
		logServer.Info(0, "new remotes list", strings.Count(in.AllRemotesDelim, ",")+1, "clientID", client.clientID, in.AllRemotesDelim)
	}

	return &pb.StartClientReply{}, nil
}

// StartCompilationSession is a grpc handler.
// A client sends this request providing sha256 of a .cpp file name and all its dependencies (.h/.nocc-pch/etc.).
// A server responds, what dependencies are missing (needed to be uploaded from the client).
// See comments in server.Session.
func (s *NoccServer) StartCompilationSession(_ context.Context, in *pb.StartCompilationSessionRequest) (*pb.StartCompilationSessionReply, error) {
	client := s.ActiveClients.GetClient(in.ClientID)
	if client == nil {
		atomic.AddInt64(&s.Stats.clientsUnauthenticated, 1)
		logServer.Error("unauthenticated client on session start", "clientID", in.ClientID)
		return nil, status.Errorf(codes.Unauthenticated, "clientID %s not found; probably, the server was restarted just now", in.ClientID)
	}

	session, err := client.CreateNewSession(in)
	if err != nil {
		atomic.AddInt64(&s.Stats.sessionsFailedOpen, 1)
		logServer.Error("failed to open session", "clientID", in.ClientID, "sessionID", in.SessionID, err)
		return nil, err
	}
	atomic.AddInt64(&s.Stats.sessionsCount, 1)

	// optimistic path: this .o has already been compiled earlier and exists in obj cache
	// then we don't need to upload files from the client (and even don't need to link them from src cache)
	// respond that we are waiting 0 files, and the client would immediately request for a compiled obj
	// it's mostly a moment of optimization: avoid calling os.Link from src cache to working dir
	if !client.disableObjCache {
		session.objCacheKey = s.ObjFileCache.MakeObjCacheKey(in.CxxName, in.CxxArgs, session.files, in.CppInFile)
		if pathInObjCache := s.ObjFileCache.LookupInCache(session.objCacheKey); len(pathInObjCache) != 0 {
			session.objCacheExists = true
			session.objOutFile = pathInObjCache // stream back this file directly
			session.compilationStarted = 1      // client.GetSessionsNotStartedCompilation() will not return it

			logServer.Info(0, "started", "sessionID", session.sessionID, "clientID", client.clientID, "from obj cache", in.CppInFile)
			client.RegisterCreatedSession(session)
			atomic.AddInt64(&s.Stats.sessionsFromObjCache, 1)
			session.PushToClientReadyChannel()

			return &pb.StartCompilationSessionReply{}, nil
		}
	}
	// otherwise, we detect files that don't exist in src cache and request a client to upload them
	// before restoring from src cache, ensure that all client dirs structure is mirrored to workingDir
	session.PrepareServerCxxCmdLine(s, in.Cwd, in.CxxArgs, in.CxxIDirs)
	client.MkdirAllForSession(session)

	// here we deal with concurrency:
	// one nocc client creates multiple sessions that depend on equal h files
	// our goal is to let the client upload file X only once:
	// the first session is responded "need X to be uploaded", whereas other sessions just wait
	// note, that if X is in src-cache, it's just hard linked from there to serverFileName
	fileIndexesToUpload := make([]uint32, 0, len(session.files))
	for index, file := range session.files {
		switch file.state {
		case fsFileStateJustCreated:
			file.state = fsFileStateUploading
			file.uploadStartTime = time.Now()

			isSystemFile := IsSystemHeaderPath(file.serverFileName) // inside /usr/local/include
			if isSystemFile && !s.SystemHeaders.IsSystemHeader(file.serverFileName, file.fileSize, file.fileSHA256) {
				return nil, fmt.Errorf("system file %s differs between a client and a server", file.serverFileName)
			}
			if isSystemFile {
				logServer.Info(2, "file", file.serverFileName, "is a system file, no need to upload")
				file.state = fsFileStateUploaded
				continue
			}
			if s.SrcFileCache.CreateHardLinkFromCache(file.serverFileName, file.fileSHA256) {
				logServer.Info(2, "file", file.serverFileName, "is in src-cache, no need to upload")
				file.state = fsFileStateUploaded

				if strings.HasSuffix(file.serverFileName, ".nocc-pch") {
					_ = s.PchCompilation.CreateHardLinkFromRealPch(file.serverFileName, file.fileSHA256)
				}
				continue
			}

			logServer.Info(1, "fs created->uploading", "sessionID", session.sessionID, client.MapServerAbsToClientFileName(file.serverFileName))
			fileIndexesToUpload = append(fileIndexesToUpload, uint32(index))

		case fsFileStateUploading:
			if !client.IsFileUploadHanged(file) { // this file is already requested to be uploaded
				continue
			}

			file.state = fsFileStateUploading
			file.uploadStartTime = time.Now()

			logServer.Error("fs uploading->uploading", "sessionID", session.sessionID, file.serverFileName, "(re-requested because previous upload hanged)")
			fileIndexesToUpload = append(fileIndexesToUpload, uint32(index))

		case fsFileStateUploadError:
			file.state = fsFileStateUploading
			file.uploadStartTime = time.Now()

			logServer.Error("fs error->uploading", "sessionID", session.sessionID, file.serverFileName, "(re-requested because previous upload error)")
			fileIndexesToUpload = append(fileIndexesToUpload, uint32(index))

		case fsFileStateUploaded:
		}
	}

	logServer.Info(0, "started", "sessionID", session.sessionID, "clientID", client.clientID, "waiting", len(fileIndexesToUpload), "uploads", in.CppInFile)
	client.RegisterCreatedSession(session)
	launchCxxOnServerOnReadySessions(s, client) // other sessions could also be waiting for files in src-cache

	return &pb.StartCompilationSessionReply{
		FileIndexesToUpload: fileIndexesToUpload,
	}, nil
}

// UploadFileStream handles a grpc stream created on a client start.
// When a client needs to upload a file, a client pushes it to the stream: so, a client is the initiator.
// Multiple .h/.cpp files are transferred over a single stream, one by one.
// This stream is alive until any error happens. On upload error, it's closed. A client recreates it on demand.
// See client.FilesUploading.
func (s *NoccServer) UploadFileStream(stream pb.CompilationService_UploadFileStreamServer) error {
	for {
		firstChunk, err := stream.Recv()
		if err != nil {
			if stream.Context().Err() != context.Canceled {
				logServer.Error("stream receive error:", err.Error())
			}
			return err
		}

		client := s.ActiveClients.GetClient(firstChunk.ClientID)
		if client == nil {
			atomic.AddInt64(&s.Stats.clientsUnauthenticated, 1)
			logServer.Error("unauthenticated client on upload stream", "clientID", firstChunk.ClientID)
			return status.Errorf(codes.Unauthenticated, "client %s not found", firstChunk.ClientID)
		}
		client.lastSeen = time.Now()

		session := client.GetSession(firstChunk.SessionID)
		if session == nil || firstChunk.FileIndex >= uint32(len(session.files)) {
			logServer.Error("bad sessionID/fileIndex on upload", "clientID", client.clientID, "sessionID", firstChunk.SessionID)
			return fmt.Errorf("unknown sessionID %d with index %d", firstChunk.SessionID, firstChunk.FileIndex)
		}

		file := session.files[firstChunk.FileIndex]
		clientFileName := session.client.MapServerAbsToClientFileName(file.serverFileName)

		if file.fileSize > 256*1024 {
			logServer.Info(0, "start receiving large file", file.fileSize, "sessionID", session.sessionID, clientFileName)
		}

		if err := receiveUploadedFileByChunks(s, stream, firstChunk, int(file.fileSize), file.serverFileName); err != nil {
			file.state = fsFileStateUploadError
			logServer.Error("fs uploading->error", "sessionID", session.sessionID, clientFileName, err)
			return fmt.Errorf("can't receive file %q: %v", clientFileName, err)
		}

		logServer.Info(2, "received", file.fileSize, "bytes", "sessionID", session.sessionID, clientFileName)
		if file.fileSize > 256*1024 {
			logServer.Info(0, "large file received", file.fileSize, "sessionID", session.sessionID, clientFileName)
		}

		// after uploading an own pch file, it's immediately compiled, resulting in .h and .gch/.pch
		if strings.HasSuffix(file.serverFileName, ".nocc-pch") {
			err = s.PchCompilation.CompileOwnPchOnServer(s, file.serverFileName)
			if err != nil {
				file.state = fsFileStateUploadError
				logServer.Error("can't compile own pch file", clientFileName, err)
				return fmt.Errorf("can't compile pch file %q: %v", clientFileName, err)
			}
		}

		file.state = fsFileStateUploaded
		logServer.Info(1, "fs uploading->uploaded", "sessionID", session.sessionID, clientFileName)
		launchCxxOnServerOnReadySessions(s, session.client) // other sessions could also be waiting for this file, we should check all
		_ = stream.Send(&pb.UploadFileReply{})
		_ = s.SrcFileCache.SaveFileToCache(file.serverFileName, path.Base(file.serverFileName), file.fileSHA256, file.fileSize)

		atomic.AddInt64(&s.Stats.bytesReceived, file.fileSize)
		atomic.AddInt64(&s.Stats.filesReceived, 1)
		// start waiting for the next file over the same stream
	}
}

// RecvCompiledObjStream handles a grpc stream created on a client start.
// When a .o file on the server is ready, it to the stream: so, a server is the initiator.
// Multiple .o files are transferred over a single stream, one by one.
// This stream is alive until any error happens. On error, it's closed. A client recreates it.
// See client.FilesReceiving.
func (s *NoccServer) RecvCompiledObjStream(in *pb.OpenReceiveStreamRequest, stream pb.CompilationService_RecvCompiledObjStreamServer) error {
	client := s.ActiveClients.GetClient(in.ClientID)
	if client == nil {
		atomic.AddInt64(&s.Stats.clientsUnauthenticated, 1)
		logServer.Error("unauthenticated client on recv stream", "clientID", in.ClientID)
		return status.Errorf(codes.Unauthenticated, "client %s not found", in.ClientID)
	}
	chunkBuf := make([]byte, 64*1024) // reusable chunk for file reading, exists until stream close

	// errors occur very rarely (if a client disconnects or something strange happens)
	// the easiest solution is just to close this stream
	// if a client is alive, it will open a new stream
	// if a trailer "sessionID" won't reach a client,
	// it would still think that a session is in the process of remote compilation
	// and will clear it after some timeout
	onError := func(sessionID uint32, format string, a ...interface{}) error {
		stream.SetTrailer(metadata.Pairs("sessionID", strconv.Itoa(int(sessionID))))
		err := fmt.Errorf(format, a...)
		logServer.Error(err)
		return err
	}

	for {
		select {
		case <-client.chanDisconnected:
			return nil

		case session := <-client.chanReadySessions:
			client.lastSeen = time.Now()

			if session.cxxExitCode != 0 {
				err := stream.Send(&pb.RecvCompiledObjChunkReply{
					SessionID:   session.sessionID,
					CxxExitCode: session.cxxExitCode,
					CxxStdout:   session.cxxStdout,
					CxxStderr:   session.cxxStderr,
					CxxDuration: session.cxxDuration,
				})
				if err != nil {
					return onError(session.sessionID, "can't send obj non-0 reply sessionID %d clientID %s %v", session.sessionID, client.clientID, err)
				}
			} else {
				logServer.Info(0, "send obj file", "sessionID", session.sessionID, "clientID", client.clientID, "cxxDuration", session.cxxDuration, session.objOutFile)
				bytesSent, err := sendObjFileByChunks(stream, chunkBuf, session)
				if err != nil {
					return onError(session.sessionID, "can't send obj file %s sessionID %d clientID %s %v", session.objOutFile, session.sessionID, client.clientID, err)
				}
				atomic.AddInt64(&s.Stats.filesSent, 1)
				atomic.AddInt64(&s.Stats.bytesSent, bytesSent)
			}

			client.CloseSession(session)
			logServer.Info(2, "close", "sessionID", session.sessionID, "clientID", client.clientID)
			// start waiting for the next ready session
		}
	}
}

// StopClient is a grpc handler. See StartClient for comments.
func (s *NoccServer) StopClient(_ context.Context, in *pb.StopClientRequest) (*pb.StopClientReply, error) {
	client := s.ActiveClients.GetClient(in.ClientID)
	if client != nil {
		logServer.Info(0, "client disconnected", "clientID", client.clientID, "; nClients", s.ActiveClients.ActiveCount()-1)
		// removing working dir could take some time, but respond immediately
		go s.ActiveClients.DeleteClient(client)
	}

	return &pb.StopClientReply{}, nil
}

// Status is a grpc handler.
// A client launched with the `-check-servers` cmd flag sends this request to all servers.
func (s *NoccServer) Status(context.Context, *pb.StatusRequest) (*pb.StatusReply, error) {
	logServer.Info(0, "requested status")

	detectVersionFromConsoleOutput := func(output []byte) string {
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, " version ") {
				return line
			}
		}
		return "not found"
	}

	gccRawOut, _ := exec.Command("g++", "-v").CombinedOutput()
	clangRawOut, _ := exec.Command("clang", "-v").CombinedOutput()
	uNameRV, _ := exec.Command("uname", "-rv").CombinedOutput()

	// var rLimit syscall.Rlimit
	// _ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)

	return &pb.StatusReply{
		ServerVersion:   common.GetVersion(),
		ServerArgs:      os.Args,
		ServerUptime:    int64(time.Since(s.StartTime)),
		GccVersion:      detectVersionFromConsoleOutput(gccRawOut),
		ClangVersion:    detectVersionFromConsoleOutput(clangRawOut),
		LogFileSize:     logServer.GetFileSize(),
		SrcCacheSize:    s.SrcFileCache.GetBytesOnDisk(),
		ObjCacheSize:    s.ObjFileCache.GetBytesOnDisk(),
		ULimit:          int64(65535),
		UName:           strings.TrimSpace(string(uNameRV)),
		SessionsTotal:   atomic.LoadInt64(&s.Stats.sessionsCount),
		SessionsActive:  s.ActiveClients.ActiveSessionsCount(),
		CxxCalls:        s.CxxLauncher.GetTotalCxxCallsCount(),
		CxxDurMore10Sec: s.CxxLauncher.GetMore10secCount(),
		CxxDurMore30Sec: s.CxxLauncher.GetMore30secCount(),
		UniqueRemotes:   s.ActiveClients.GetUniqueRemotesListInfo(),
	}, nil
}

// DumpLogs is a grpc handler.
// A client launched with the `-dump-server-logs` cmd flag sends this request to all servers.
func (s *NoccServer) DumpLogs(_ *pb.DumpLogsRequest, stream pb.CompilationService_DumpLogsServer) error {
	logServer.Info(0, "requested to dump logs")

	currentLog := logServer.GetFileName()
	if currentLog == "" {
		return errors.New("can't dump logs, as they aren't being saved to file")
	}

	// current: nocc-server.log
	err := sendLogFileByChunks(stream, currentLog, ".log")
	if err != nil {
		return err
	}
	// previous rotated: nocc-server.log.1.gz
	_ = sendLogFileByChunks(stream, currentLog+".1.gz", ".log.1.gz")
	// stderr, for crashes: nocc-server.err.log
	_ = sendLogFileByChunks(stream, common.ReplaceFileExt(currentLog, ".err.log"), ".log.err")

	// empty, end of stream
	return stream.Send(&pb.DumpLogsReply{LogFileExt: ""})
}

// DropAllCaches drops src and obj caches without restarting a server.
// Used primarily for development purposes.
func (s *NoccServer) DropAllCaches(context.Context, *pb.DropAllCachesRequest) (*pb.DropAllCachesReply, error) {
	logServer.Info(0, "requested to drop all caches")

	droppedSrcFiles := s.SrcFileCache.GetFilesCount()
	droppedObjFiles := s.ObjFileCache.GetFilesCount()
	s.SrcFileCache.DropAll()
	s.ObjFileCache.DropAll()

	return &pb.DropAllCachesReply{
		DroppedSrcFiles: droppedSrcFiles,
		DroppedObjFiles: droppedObjFiles,
	}, nil
}

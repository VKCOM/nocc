package client

import (
	"context"
	"fmt"
	"strings"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/VKCOM/nocc/pb"
)

// RemoteConnection represents a state of a current process related to remote execution.
// It also has methods that call grpc, so this module is close to protobuf.
// Daemon makes one RemoteConnection to one server — for server.Session creation, files uploading, obj receiving.
// If a remote is not available on daemon start (on becomes unavailable in the middle),
// then all invocations that should be sent to that remote are executed locally within a daemon.
type RemoteConnection struct {
	remoteHostPort string
	remoteHost     string // for console output and logs, just IP is more pretty
	isUnavailable  bool

	grpcClient     *GRPCClient
	filesUploading *FilesUploading
	filesReceiving *FilesReceiving

	clientID        string // = Daemon.clientID
	hostUserName    string // = Daemon.hostUserName
	disableObjCache bool
}

func ExtractRemoteHostWithoutPort(remoteHostPort string) (remoteHost string) {
	remoteHost = remoteHostPort
	if idx := strings.Index(remoteHostPort, ":"); idx != -1 {
		remoteHost = remoteHostPort[:idx]
	}
	return
}

func MakeRemoteConnection(daemon *Daemon, remoteHostPort string, ctxWithTimeout context.Context) (*RemoteConnection, error) {
	grpcClient, err := MakeGRPCClient(remoteHostPort)

	remote := &RemoteConnection{
		remoteHostPort:  remoteHostPort,
		remoteHost:      ExtractRemoteHostWithoutPort(remoteHostPort),
		grpcClient:      grpcClient,
		filesUploading:  MakeFilesUploading(daemon, grpcClient),
		filesReceiving:  MakeFilesReceiving(daemon, grpcClient),
		clientID:        daemon.clientID,
		hostUserName:    daemon.hostUserName,
		disableObjCache: daemon.disableObjCache,
	}

	if err != nil {
		return remote, err
	}

	_, err = grpcClient.pb.StartClient(ctxWithTimeout, &pb.StartClientRequest{
		ClientID:        daemon.clientID,
		HostUserName:    daemon.hostUserName,
		ClientVersion:   common.GetVersion(),
		DisableObjCache: daemon.disableObjCache,
		AllRemotesDelim: daemon.allRemotesDelim, // just to log on a server-side
	})
	if err != nil {
		return remote, err
	}

	if err := remote.filesUploading.CreateUploadStream(); err != nil {
		return remote, err
	}

	if err := remote.filesReceiving.CreateReceiveStream(); err != nil {
		return remote, err
	}

	return remote, nil
}

// StartCompilationSession starts a session on the remote:
// one `nocc` Invocation for cpp compilation == one server.Session, by design.
// As an input, we send metadata about all dependencies needed for a .cpp to be compiled (.h/.nocc-pch/etc.).
// As an output, the remote responds with files that are missing and needed to be uploaded.
func (remote *RemoteConnection) StartCompilationSession(invocation *Invocation, cwd string, requiredFiles []*pb.FileMetadata) ([]uint32, error) {
	if remote.isUnavailable {
		return nil, fmt.Errorf("remote %s is unavailable", remote.remoteHost)
	}

	startSessionReply, err := remote.grpcClient.pb.StartCompilationSession(
		remote.grpcClient.callContext,
		&pb.StartCompilationSessionRequest{
			ClientID:      remote.clientID,
			SessionID:     invocation.sessionID,
			Cwd:           cwd,
			CppInFile:     invocation.cppInFile,
			CxxName:       invocation.cxxName,
			CxxArgs:       invocation.cxxArgs,
			CxxIDirs:      append(invocation.cxxIDirs.AsCxxArgs(), invocation.includesCache.cxxDefIDirs.AsCxxArgs()...),
			RequiredFiles: requiredFiles,
		})
	if err != nil {
		return nil, err
	}

	return startSessionReply.FileIndexesToUpload, nil
}

// UploadFilesToRemote uploads files to the remote in parallel and finishes after all of them are done.
func (remote *RemoteConnection) UploadFilesToRemote(invocation *Invocation, requiredFiles []*pb.FileMetadata, fileIndexesToUpload []uint32) error {
	invocation.waitUploads = int32(len(fileIndexesToUpload))
	invocation.wgUpload.Add(int(invocation.waitUploads))

	for _, fileIndex := range fileIndexesToUpload {
		remote.filesUploading.StartUploadingFileToRemote(invocation, requiredFiles[fileIndex], fileIndex)
	}

	invocation.wgUpload.Wait()
	return invocation.err
}

// WaitForCompiledObj returns when the resulting .o file is compiled on remote, downloaded and saved on client.
// We don't send any request here, just wait: after all uploads finish, the remote starts compiling .cpp.
// When .o is ready, the remote pushes it to a receiving stream, and wgRecv is done.
// If cxx compilation exits with non-zero code, the same stream is used to send error details.
// See FilesReceiving.
func (remote *RemoteConnection) WaitForCompiledObj(invocation *Invocation) (exitCode int, stdout []byte, stderr []byte, err error) {
	invocation.wgRecv.Wait()

	return invocation.cxxExitCode, invocation.cxxStdout, invocation.cxxStderr, invocation.err
}

func (remote *RemoteConnection) SendStopClient(ctxSmallTimeout context.Context) {
	if remote.isUnavailable {
		return
	}
	_, _ = remote.grpcClient.pb.StopClient(
		ctxSmallTimeout,
		&pb.StopClientRequest{
			ClientID: remote.clientID,
		})
}

func (remote *RemoteConnection) Clear() {
	remote.grpcClient.Clear()
}

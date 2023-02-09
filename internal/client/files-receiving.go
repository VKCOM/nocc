package client

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/VKCOM/nocc/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FilesReceiving is a singleton inside Daemon that holds a bunch of grpc streams to receive compiled .o files.
// The number of streams is limited, they all are initialized on daemon start.
// When another .o is ready, it's pushed by the server (a client only receives, it doesn't send anything back).
type FilesReceiving struct {
	daemon     *Daemon
	grpcClient *GRPCClient
}

func MakeFilesReceiving(daemon *Daemon, grpcClient *GRPCClient) *FilesReceiving {
	return &FilesReceiving{
		daemon:     daemon,
		grpcClient: grpcClient,
	}
}

func (fr *FilesReceiving) CreateReceiveStream() error {
	ctx, cancelFunc := context.WithCancel(context.Background())
	stream, err := fr.grpcClient.pb.RecvCompiledObjStream(ctx,
		&pb.OpenReceiveStreamRequest{ClientID: fr.daemon.clientID},
	)
	if err != nil {
		cancelFunc()
		return err
	}

	go fr.monitorRemoteStreamForObjReceiving(stream, cancelFunc)
	return nil
}

func (fr *FilesReceiving) RecreateReceiveStreamOrQuit(failedStreamCancelFunc context.CancelFunc, err error) {
	failedStreamCancelFunc() // will close the stream on the server also
	logClient.Error("recreate recv stream:", err)
	time.Sleep(100 * time.Millisecond)

	if err := fr.CreateReceiveStream(); err != nil {
		fr.daemon.OnRemoteBecameUnavailable(fr.grpcClient.remoteHostPort, err)
	}
}

// monitorRemoteStreamForObjReceiving listens to a grpc receiving stream and handles .o files sent by a remote.
// When a next .o is ready on remote, it sends it to a stream.
// One stream is used to receive multiple .o files consecutively.
// If cxx compilation exits with non-zero code, the same stream is used to send error details.
// See RemoteConnection.WaitForCompiledObj.
func (fr *FilesReceiving) monitorRemoteStreamForObjReceiving(stream pb.CompilationService_RecvCompiledObjStreamClient, cancelFunc context.CancelFunc) {
	for {
		firstChunk, err := stream.Recv()

		// such complexity of error handling prevents hanging sessions and proper stream recreation
		if err != nil {
			// when a daemon quits, all streams are automatically closed
			select {
			case <-fr.daemon.quitChan:
				return
			default:
				break
			}

			// grpc stream creation doesn't wait for ack, that's why
			// if a stream couldn't be created at all, we know this only on Recv() failure
			if st, ok := status.FromError(err); ok {
				if st.Code() == codes.Unauthenticated {
					fr.daemon.OnRemoteBecameUnavailable(fr.grpcClient.remoteHostPort, err)
					return
				}
			}

			// if something weird occurs, the server fails to send a chunk to a stream
			// it closes the stream and includes metadata to trailer
			// here, on the client size, we mark this invocation as errored, they'll be compiled locally
			// this prevents invocations from hanging — at least when a network works as expected
			mdSession := stream.Trailer().Get("sessionID")
			if len(mdSession) == 1 {
				sessionID, _ := strconv.Atoi(mdSession[0])
				invocation := fr.daemon.FindBySessionID(uint32(sessionID))
				if invocation != nil {
					invocation.DoneRecvObj(err)
				}
			}

			// NB: there are rpc errors that are not visible to the server-side (like codes.ResourceExhausted)
			// in this case, the server thinks that .o was sent, but the client gets an error without metadata
			// such invocations will be cleared later, see PeriodicallyInterruptHangedInvocations()
			fr.RecreateReceiveStreamOrQuit(cancelFunc, err)
			return
		}

		invocation := fr.daemon.FindBySessionID(firstChunk.SessionID)
		if invocation == nil {
			logClient.Error("can't find invocation for obj", "sessionID", firstChunk.SessionID)
			if firstChunk.CxxExitCode == 0 {
				if err, _ = receiveObjFileByChunks(stream, firstChunk, "/tmp/nocc-dev-null"); err != nil {
					fr.RecreateReceiveStreamOrQuit(cancelFunc, err)
					return
				}
			}
			continue
		}

		invocation.cxxExitCode = int(firstChunk.CxxExitCode)
		invocation.cxxStdout = firstChunk.CxxStdout
		invocation.cxxStderr = firstChunk.CxxStderr
		invocation.cxxDuration = firstChunk.CxxDuration
		invocation.summary.nBytesReceived += int(firstChunk.FileSize)

		// non-zero cxxExitCode means a bug in cpp source code and doesn't require local fallback
		if firstChunk.CxxExitCode != 0 {
			invocation.DoneRecvObj(nil)
			continue
		}

		err, needRecreateStream := receiveObjFileByChunks(stream, firstChunk, invocation.objOutFile)
		invocation.DoneRecvObj(err)

		// recreate a stream if it's corrupted, like chunks mismatch
		// (if so, invocation won't be left hanged, as it's already errored)
		if err != nil && needRecreateStream {
			fr.RecreateReceiveStreamOrQuit(cancelFunc, err)
			return
		}

		// continue waiting for next .o files pushed by the remote over the same stream
	}
}

// receiveObjFileByChunks is an actual implementation of saving a server stream to a local client .o file.
// See server.sendObjFileByChunks.
func receiveObjFileByChunks(stream pb.CompilationService_RecvCompiledObjStreamClient, firstChunk *pb.RecvCompiledObjChunkReply, objOutFile string) (error, bool) {
	receivedBytes := len(firstChunk.ChunkBody)
	expectedBytes := int(firstChunk.FileSize)

	var errWrite error
	var errRecv error

	if receivedBytes >= expectedBytes {
		// if a dir for objOutFile doesn't exist, it will fail; g++/clang act the same
		errWrite = os.WriteFile(objOutFile, firstChunk.ChunkBody, os.ModePerm)
		return errWrite, false
	}

	fileTmp, errWrite := common.OpenTempFile(objOutFile)
	if errWrite == nil {
		_, errWrite = fileTmp.Write(firstChunk.ChunkBody)
	}

	var nextChunk *pb.RecvCompiledObjChunkReply
	for receivedBytes < expectedBytes {
		nextChunk, errRecv = stream.Recv()
		if errRecv != nil { // EOF is also unexpected
			break
		}
		if errWrite == nil {
			_, errWrite = fileTmp.Write(nextChunk.ChunkBody)
		}
		if nextChunk.SessionID != firstChunk.SessionID {
			errRecv = fmt.Errorf("inconsistent stream, chunks mismatch")
			break
		}
		receivedBytes += len(nextChunk.ChunkBody)
	}

	if fileTmp != nil {
		_ = fileTmp.Close()
		if errWrite == nil {
			errWrite = os.Rename(fileTmp.Name(), objOutFile)
		}
		_ = os.Remove(fileTmp.Name())
	}

	switch {
	case errRecv != nil:
		return errRecv, true // "true" to recreate recv stream
	case errWrite != nil:
		return errWrite, false // "false" means that the stream is ok, there was just a problem of saving a file
	default:
		return nil, false
	}
}

// receiveLogFileByChunks gets a server log file and saves to a client file system, for debugging purposes
// (implementation is simpler than receiving obj file, don't bother with proper error handling).
// See server.sendLogFileByChunks.
func receiveLogFileByChunks(stream pb.CompilationService_DumpLogsClient, firstChunk *pb.DumpLogsReply, logOutFile string) (int, error) {
	receivedBytes := len(firstChunk.ChunkBody)

	outFile, err := os.OpenFile(logOutFile, os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err == nil {
		_, err = outFile.Write(firstChunk.ChunkBody)
	}

	var nextChunk *pb.DumpLogsReply
	for err == nil {
		nextChunk, err = stream.Recv()
		if err != nil || nextChunk.ChunkBody == nil { // nil chunk means end of file
			break
		}
		_, err = outFile.Write(nextChunk.ChunkBody)
		receivedBytes += len(nextChunk.ChunkBody)
	}

	if outFile != nil {
		_ = outFile.Close()
	}

	return receivedBytes, err // err remains nil on success
}

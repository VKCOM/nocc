package client

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/VKCOM/nocc/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fileUploadReq struct {
	invocation *Invocation
	file       *pb.FileMetadata
	fileIndex  uint32
}

// FilesUploading is a singleton inside Daemon that holds a bunch of grpc streams to upload .cpp/.h files.
// Very similar to FilesReceiving.
type FilesUploading struct {
	daemon       *Daemon
	grpcClient   *GRPCClient
	chanToUpload chan fileUploadReq
}

func MakeFilesUploading(daemon *Daemon, grpcClient *GRPCClient) *FilesUploading {
	return &FilesUploading{
		daemon:       daemon,
		grpcClient:   grpcClient,
		chanToUpload: make(chan fileUploadReq, 50),
	}
}

func (fu *FilesUploading) CreateUploadStream() error {
	ctx, cancelFunc := context.WithCancel(context.Background())
	stream, err := fu.grpcClient.pb.UploadFileStream(ctx)
	if err != nil {
		cancelFunc()
		return err
	}

	go fu.monitorClientChanForFileUploading(stream, cancelFunc)
	return nil
}

func (fu *FilesUploading) RecreateUploadStreamOrQuit(failedStreamCancelFunc context.CancelFunc, err error) {
	failedStreamCancelFunc()
	logClient.Error("recreate upload stream:", err)
	time.Sleep(100 * time.Millisecond)

	if err := fu.CreateUploadStream(); err != nil {
		fu.daemon.OnRemoteBecameUnavailable(fu.grpcClient.remoteHostPort, err)
	}
}

func (fu *FilesUploading) StartUploadingFileToRemote(invocation *Invocation, file *pb.FileMetadata, fileIndex uint32) {
	fu.chanToUpload <- fileUploadReq{
		invocation: invocation,
		file:       file,
		fileIndex:  fileIndex,
	}
}

// monitorClientChanForFileUploading listens to chanToUpload and uploads it via stream.
// One grpc stream is used to upload multiple files consecutively.
func (fu *FilesUploading) monitorClientChanForFileUploading(stream pb.CompilationService_UploadFileStreamClient, cancelFunc context.CancelFunc) {
	chunkBuf := make([]byte, 64*1024) // reusable chunk for file reading, exists until stream close

	for {
		select {
		case <-fu.daemon.quitChan:
			return

		case req := <-fu.chanToUpload:
			logClient.Info(2, "start uploading", req.file.FileSize, req.file.ClientFileName)
			if req.file.FileSize > 64*1024 {
				logClient.Info(1, "upload large file", req.file.FileSize, req.file.ClientFileName)
			}

			invocation := req.invocation
			err := uploadFileByChunks(stream, chunkBuf, req.file.ClientFileName, fu.daemon.clientID, invocation.sessionID, req.fileIndex)

			// such complexity of error handling prevents hanging sessions and proper stream recreation
			if err != nil {
				// when a daemon quits, all streams are automatically closed
				select {
				case <-fu.daemon.quitChan:
					return
				default:
					break
				}

				// if something goes completely wrong and stream recreation fails, mark this remote as unavailable
				// see FilesReceiving for a comment about this error code
				if st, ok := status.FromError(err); ok {
					if st.Code() == codes.Unauthenticated {
						fu.daemon.OnRemoteBecameUnavailable(fu.grpcClient.remoteHostPort, err)
						return
					}
				}

				// if some error occurred, the stream could be left in the middle of uploading
				// the easiest solution is to close this stream and to reopen a new one
				// if the server became inaccessible, recreation would fail
				fu.RecreateUploadStreamOrQuit(cancelFunc, err)

				// theoretically, we could implement retries: if something does wrong with the network,
				// then retry uploading (by pushing req to fu.chanToUpload)
				// to do this correctly, we need to distinguish network errors vs file errors (and don't retry then)
				// for now, there are no retries: if something fails, this invocation will be executed locally
				invocation.DoneUploadFile(err)
				return
			}

			invocation.summary.nFilesSent++
			invocation.summary.nBytesSent += int(req.file.FileSize)
			invocation.DoneUploadFile(nil)
			// continue listening, reuse the same stream to upload new files
		}
	}
}

// uploadFileByChunks is an actual implementation of piping a local client file to a server stream.
// See server.receiveUploadedFileByChunks.
func uploadFileByChunks(stream pb.CompilationService_UploadFileStreamClient, chunkBuf []byte, clientFileName string, clientID string, sessionID uint32, fileIndex uint32) error {
	fd, err := os.Open(clientFileName)
	if err != nil {
		return err
	}
	defer fd.Close()

	var n int
	var sentChunks = 0 // used to correctly handle empty files (when Read returns EOF immediately)
	for {
		n, err = fd.Read(chunkBuf)
		if err != nil && err != io.EOF {
			return err
		}
		if err == io.EOF && sentChunks != 0 {
			break
		}
		sentChunks++

		err = stream.Send(&pb.UploadFileChunkRequest{
			ClientID:  clientID,
			SessionID: sessionID,
			FileIndex: fileIndex,
			ChunkBody: chunkBuf[:n],
		})
		if err != nil {
			return err
		}
	}

	// when a file uploaded succeeds, the server sends just an empty confirmation packet
	// if the server couldn't save an uploaded file, it would return an error (and the stream will be recreated)
	_, err = stream.Recv()
	return err
}

package server

import (
	"fmt"
	"io"
	"os"

	"github.com/VKCOM/nocc/pb"
)

// receiveUploadedFileByChunks is an actual implementation of piping a client stream to a local server file.
// See client.uploadFileByChunks.
func receiveUploadedFileByChunks(noccServer *NoccServer, stream pb.CompilationService_UploadFileStreamServer, firstChunk *pb.UploadFileChunkRequest, expectedBytes int, serverFileName string) (err error) {
	receivedBytes := len(firstChunk.ChunkBody)

	// we write to a tmp file and rename it to serverFileName after saving
	// it prevents races from concurrent writing to the same file
	// (this situation is possible on a slow network when a file was requested several times)
	fileTmp, err := noccServer.SrcFileCache.MakeTempFileForUploadSaving(serverFileName)
	if err == nil {
		_, err = fileTmp.Write(firstChunk.ChunkBody)
	}

	var nextChunk *pb.UploadFileChunkRequest
	for receivedBytes < expectedBytes && err == nil {
		nextChunk, err = stream.Recv()
		if err != nil { // EOF is also unexpected
			break
		}
		_, err = fileTmp.Write(nextChunk.ChunkBody)
		if nextChunk.SessionID != firstChunk.SessionID || nextChunk.FileIndex != firstChunk.FileIndex {
			err = fmt.Errorf("inconsistent stream, chunks mismatch")
		}
		receivedBytes += len(nextChunk.ChunkBody)
	}

	if fileTmp != nil {
		_ = fileTmp.Close()
		if err == nil {
			err = os.Rename(fileTmp.Name(), serverFileName)
		}
		if err != nil {
			_ = os.Remove(fileTmp.Name())
		}
	}
	return
}

// sendObjFileByChunks is an actual implementation of piping a local server file to a client stream.
// See client.receiveObjFileByChunks.
func sendObjFileByChunks(stream pb.CompilationService_RecvCompiledObjStreamServer, chunkBuf []byte, session *Session) (int64, error) {
	fd, err := os.Open(session.objOutFile)
	if err != nil {
		return 0, err
	}
	defer fd.Close()
	stat, err := fd.Stat()
	if err != nil {
		return 0, err
	}

	var n int
	for {
		n, err = fd.Read(chunkBuf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		err = stream.Send(&pb.RecvCompiledObjChunkReply{
			SessionID:   session.sessionID,
			CxxExitCode: session.cxxExitCode,
			CxxStdout:   session.cxxStdout,
			CxxStderr:   session.cxxStderr,
			CxxDuration: session.cxxDuration,
			FileSize:    stat.Size(),
			ChunkBody:   chunkBuf[:n],
		})
		if err != nil {
			return 0, err
		}
	}

	// after sending a compiled obj, the client doesn't respond in any way,
	// so we don't call stream.Recv(), the stream is already ready to send other objs
	return stat.Size(), nil
}

// sendLogFileByChunks streams a local server log file, for debugging purposes
// (implementation is similar to streaming obj file, but made simpler).
// See client.receiveLogFileByChunks.
func sendLogFileByChunks(stream pb.CompilationService_DumpLogsServer, serverLogFileName string, clientLogExt string) error {
	chunkBuf := make([]byte, 1024*1024)
	fd, err := os.Open(serverLogFileName)
	if err != nil {
		return err
	}
	defer fd.Close()

	var n int
	for err == nil {
		n, err = fd.Read(chunkBuf)
		if err == io.EOF {
			break
		}

		err = stream.Send(&pb.DumpLogsReply{
			LogFileExt: clientLogExt,
			ChunkBody:  chunkBuf[:n],
		})
	}

	return stream.Send(&pb.DumpLogsReply{ChunkBody: nil}) // nil chunk means end of file
}

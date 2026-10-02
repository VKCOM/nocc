package client

import (
	"os"
	"testing"

	"github.com/VKCOM/nocc/pb"
	"google.golang.org/grpc"
)

func TestMain(m *testing.M) {
	if err := MakeLoggerClient("", -1, true); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

type fakeRecvStream struct {
	grpc.ClientStream
	replies chan *pb.RecvCompiledObjChunkReply
}

func (f *fakeRecvStream) Recv() (*pb.RecvCompiledObjChunkReply, error) {
	return <-f.replies, nil
}

// A server that predates its own check may still report a killed compiler as exit code -1;
// the client must take that as a remote failure (and fall back), not as a compile error to show.
func TestKilledRemoteCompilerIsARemoteFailure(t *testing.T) {
	daemon := &Daemon{quitChan: make(chan int), activeInvocations: make(map[uint32]*Invocation)}
	invocation := &Invocation{sessionID: 5, summary: MakeInvocationSummary()}
	invocation.wgRecv.Add(1)
	daemon.activeInvocations[invocation.sessionID] = invocation

	stream := &fakeRecvStream{replies: make(chan *pb.RecvCompiledObjChunkReply, 1)}
	stream.replies <- &pb.RecvCompiledObjChunkReply{SessionID: 5, CxxExitCode: -1, CxxStderr: []byte("signal: terminated")}
	go (&FilesReceiving{daemon: daemon}).monitorRemoteStreamForObjReceiving(stream, func() {})

	invocation.wgRecv.Wait()
	if invocation.err == nil {
		t.Fatalf("exit code %d was taken as a compile result, so it would never be compiled elsewhere", invocation.cxxExitCode)
	}
}

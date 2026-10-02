package server

import (
	"os"
	"testing"
	"time"

	"github.com/VKCOM/nocc/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestMain(m *testing.M) {
	if err := MakeLoggerServer("", -1); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

type fakeObjStream struct {
	grpc.ServerStream
	sent    []*pb.RecvCompiledObjChunkReply
	trailer metadata.MD
}

func (f *fakeObjStream) Send(reply *pb.RecvCompiledObjChunkReply) error {
	f.sent = append(f.sent, reply)
	return nil
}

func (f *fakeObjStream) SetTrailer(md metadata.MD) {
	f.trailer = md
}

func newTestServerWithClient(t *testing.T) (*NoccServer, *Client) {
	t.Helper()
	storage, _ := MakeClientsStorage(t.TempDir(), time.Minute)
	client, err := storage.OnClientConnected("c1", false)
	if err != nil {
		t.Fatal(err)
	}
	return &NoccServer{ActiveClients: storage}, client
}

// A compiler killed under the server (the server stopping, the OOM killer) is not a compile error
// in the client's source; it must reach the client as a remote failure, like a broken connection.
func TestKilledCompilerIsNotReportedAsCompileError(t *testing.T) {
	s, client := newTestServerWithClient(t)
	client.chanReadySessions <- &Session{sessionID: 7, cxxExitCode: 1, cxxStderr: []byte("g++: fatal error: Killed signal terminated program cc1plus\ncompilation terminated.")}

	stream := &fakeObjStream{}
	done := make(chan error)
	go func() { done <- s.RecvCompiledObjStream(&pb.OpenReceiveStreamRequest{ClientID: "c1"}, stream) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Second): // the stream went on waiting for more sessions; end it to look at what was sent
		s.ActiveClients.DeleteClient(client)
		err = <-done
	}

	if len(stream.sent) != 0 {
		t.Fatalf("sent a reply with exit code %d, which the client takes for a compile error", stream.sent[0].CxxExitCode)
	}
	if err == nil {
		t.Fatal("the stream didn't fail, so the client never learns this session failed")
	}
	if got := stream.trailer.Get("sessionID"); len(got) != 1 || got[0] != "7" {
		t.Errorf("trailer sessionID = %v, want [7]: without it, the client can't tell which invocation failed", got)
	}
}

// A real compile error still goes through as one.
func TestCompileErrorIsStillReported(t *testing.T) {
	s, client := newTestServerWithClient(t)
	client.chanReadySessions <- &Session{sessionID: 8, cxxExitCode: 1, cxxStderr: []byte("error: expected ';'")}

	stream := &fakeObjStream{}
	done := make(chan error)
	go func() { done <- s.RecvCompiledObjStream(&pb.OpenReceiveStreamRequest{ClientID: "c1"}, stream) }()
	time.Sleep(100 * time.Millisecond)
	s.ActiveClients.DeleteClient(client)

	if err := <-done; err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if len(stream.sent) != 1 || stream.sent[0].CxxExitCode != 1 {
		t.Fatalf("got %v, want one reply with exit code 1", stream.sent)
	}
}

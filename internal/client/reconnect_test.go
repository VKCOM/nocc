package client

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func newTestDaemonWithRemote() (*Daemon, *RemoteConnection) {
	remote := &RemoteConnection{remoteHostPort: "host0:43210", remoteHost: "host0", grpcClient: &GRPCClient{}}
	daemon := &Daemon{
		remoteConnections: []*RemoteConnection{remote},
		activeInvocations: make(map[uint32]*Invocation),
	}
	return daemon, remote
}

// A stream of a connection that a reconnect already replaced still fails on its way out;
// that must not take down the fresh connection to the same host.
func TestStaleConnectionDoesNotMarkItsSuccessorUnavailable(t *testing.T) {
	daemon, remote := newTestDaemonWithRemote()
	replacedGrpcClient := &GRPCClient{}

	daemon.OnRemoteBecameUnavailable(replacedGrpcClient, errors.New("stream of a replaced connection"))
	if remote.isUnavailable.Load() {
		t.Fatal("a replaced connection marked the current one to the same host unavailable")
	}

	daemon.OnRemoteBecameUnavailable(remote.grpcClient, errors.New("stream broke"))
	if !remote.isUnavailable.Load() {
		t.Fatal("the current connection wasn't marked unavailable by its own stream")
	}
}

// A compilation in flight on a remote that went down must fail right away (to be retried),
// not wait for forceInterruptTimeout.
func TestRemoteGoingDownInterruptsItsInvocations(t *testing.T) {
	daemon, remote := newTestDaemonWithRemote()
	other := &RemoteConnection{remoteHost: "host1", grpcClient: &GRPCClient{}}

	onRemote := &Invocation{sessionID: 1, remote: remote, summary: MakeInvocationSummary()}
	onOther := &Invocation{sessionID: 2, remote: other, summary: MakeInvocationSummary()}
	for _, invocation := range []*Invocation{onRemote, onOther} {
		invocation.wgRecv.Add(1)
		daemon.activeInvocations[invocation.sessionID] = invocation
	}

	daemon.OnRemoteBecameUnavailable(remote.grpcClient, errors.New("stream broke"))

	released := make(chan struct{})
	go func() { onRemote.wgRecv.Wait(); close(released) }()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("an invocation on the remote that went down is still waiting for its .o")
	}
	if onRemote.err == nil {
		t.Error("the interrupted invocation has no error, so it would be taken as compiled")
	}
	if onOther.err != nil {
		t.Error("an invocation on another remote was interrupted too")
	}
}

// ForceInterrupt drains pending uploads while the upload stream may still be finishing them;
// together they must never release wgUpload more times than it was added (that panics).
func TestUploadsFinishingDuringInterruptDontPanic(t *testing.T) {
	for i := 0; i < 1000; i++ {
		invocation := &Invocation{summary: MakeInvocationSummary()}
		invocation.waitUploads = 5
		invocation.wgUpload.Add(5)
		invocation.wgRecv.Add(1) // as CompileCppRemotely does; ForceInterrupt releases it too

		var wg sync.WaitGroup
		wg.Add(6)
		for j := 0; j < 5; j++ {
			go func() { defer wg.Done(); invocation.DoneUploadFile(nil) }()
		}
		go func() { defer wg.Done(); invocation.ForceInterrupt(errors.New("interrupted")) }()
		wg.Wait()
		invocation.wgUpload.Wait()
	}
}

func TestRemoteRetryDelay(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for attempt, delay := range want {
		if got := remoteRetryDelay(attempt); got != delay {
			t.Errorf("attempt %d: got %v, want %v", attempt, got, delay)
		}
	}
}

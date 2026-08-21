package client

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// routing decisions are logged; without this, logClient is nil
	if err := MakeLoggerClient("", -1, true); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func newTestRemotes(n int) []*RemoteConnection {
	conns := make([]*RemoteConnection, n)
	for i := range conns {
		conns[i] = &RemoteConnection{remoteHost: fmt.Sprintf("host%d", i)}
	}
	return conns
}

// A server that can't be the compiler we asked for must not swallow its share of the build:
// those files go to another server, not to the (slow) local machine.
func TestFilesMoveOffAServerThatCantCompileThem(t *testing.T) {
	conns := newTestRemotes(3)
	daemon := &Daemon{remoteConnections: conns}
	conns[1].MarkIncapableOfCxx("arm-linux-gnueabihf-g++", "not installed there")

	sentToIncapable := 0
	sentToLocal := 0
	for i := 0; i < 3000; i++ {
		remote := daemon.chooseRemoteConnectionForCppCompilation(fmt.Sprintf("file%d.cpp", i), "arm-linux-gnueabihf-g++")
		if remote == nil {
			sentToLocal++
		} else if remote == conns[1] {
			sentToIncapable++
		}
	}
	if sentToIncapable != 0 || sentToLocal != 0 {
		t.Errorf("%d files went to the incapable server, %d fell back to local", sentToIncapable, sentToLocal)
	}
}

// Being unable to serve one compiler says nothing about another one.
func TestIncapabilityIsPerCompiler(t *testing.T) {
	conns := newTestRemotes(2)
	daemon := &Daemon{remoteConnections: conns}
	conns[0].MarkIncapableOfCxx("clang++", "not installed there")

	usedForGcc := make(map[*RemoteConnection]bool)
	for i := 0; i < 2000; i++ {
		usedForGcc[daemon.chooseRemoteConnectionForCppCompilation(fmt.Sprintf("file%d.cpp", i), "g++")] = true
	}
	if !usedForGcc[conns[0]] {
		t.Error("a server refused for clang++ is no longer used for g++ either")
	}
}

// Only the displaced files move. Everything else keeps its server, so the other servers'
// caches stay warm — the whole reason the choice is a hash in the first place.
func TestOtherFilesKeepTheirServer(t *testing.T) {
	conns := newTestRemotes(4)
	daemon := &Daemon{remoteConnections: conns}

	before := make([]*RemoteConnection, 2000)
	for i := range before {
		before[i] = daemon.chooseRemoteConnectionForCppCompilation(fmt.Sprintf("file%d.cpp", i), "g++")
	}

	conns[2].MarkIncapableOfCxx("g++", "not installed there")

	moved, movedOffTheIncapable := 0, 0
	for i := range before {
		after := daemon.chooseRemoteConnectionForCppCompilation(fmt.Sprintf("file%d.cpp", i), "g++")
		if after != before[i] {
			moved++
			if before[i] == conns[2] {
				movedOffTheIncapable++
			}
		}
	}
	if moved == 0 {
		t.Fatal("nothing moved off the incapable server")
	}
	if moved != movedOffTheIncapable {
		t.Errorf("%d files moved, but only %d of them were on the incapable server", moved, movedOffTheIncapable)
	}
}

func TestUnavailableServersAreSkippedToo(t *testing.T) {
	conns := newTestRemotes(2)
	daemon := &Daemon{remoteConnections: conns}
	conns[0].isUnavailable = true

	for i := 0; i < 500; i++ {
		if remote := daemon.chooseRemoteConnectionForCppCompilation(fmt.Sprintf("file%d.cpp", i), "g++"); remote != conns[1] {
			t.Fatalf("file%d.cpp went to %v, not to the only available server", i, remote)
		}
	}
}

// With nothing left to compile on, the caller has to know — that's what makes it
// fall back to a local compilation instead of dialing a server that refused it.
func TestNoUsableServerReturnsNil(t *testing.T) {
	conns := newTestRemotes(2)
	daemon := &Daemon{remoteConnections: conns}
	conns[0].isUnavailable = true
	conns[1].MarkIncapableOfCxx("g++", "not installed there")

	if remote := daemon.chooseRemoteConnectionForCppCompilation("1.cpp", "g++"); remote != nil {
		t.Errorf("got %v, want nil", remote)
	}
}

package client

import (
	"fmt"
	"strconv"
	"testing"
)

func makeDaemonWithRemotes(numRemotes int) *Daemon {
	conns := make([]*RemoteConnection, numRemotes)
	for i := range conns {
		conns[i] = &RemoteConnection{remoteHost: fmt.Sprintf("host%d", i)}
	}
	return &Daemon{remoteConnections: conns}
}

// chooseRemoteConnectionForCppCompilation used to reduce the FNV hash in `int`:
//
//	daemon.remoteConnections[int(hasher.Sum32())%len(daemon.remoteConnections)]
//
// Where `int` is 32 bits, that conversion is negative for every hash with the top
// bit set -- about half of all file names -- and Go's % keeps the sign of the
// dividend, so the index could come out negative and panic the daemon. It is not
// every such name: with n remotes the result is negative unless the hash is a
// multiple of n, so (n-1)/2n of all names panic, a quarter of them at n == 2.
// A single remote masked it completely, because x%1 == 0 for any x.
//
// This is the arch-specific half of the guard, and it is honest about it: on a
// 64-bit host the old code cannot produce a negative index, so the test would
// pass against the bug and prove nothing. Skip rather than report a green that
// did not exercise anything. Run the suite under GOARCH=386 or GOARCH=arm to
// actually execute this.
func TestChooseRemoteConnectionIndexIsNonNegativeOn32Bit(t *testing.T) {
	if strconv.IntSize != 32 {
		t.Skipf("int is %d bits here; this regression only reproduces where int is 32 bits (GOARCH=386, GOARCH=arm)", strconv.IntSize)
	}

	for _, numRemotes := range []int{2, 3, 4, 5, 8} {
		daemon := makeDaemonWithRemotes(numRemotes)
		for i := 0; i < 20000; i++ {
			cppInFile := fmt.Sprintf("src/file%d.cpp", i)
			if remote := daemon.chooseRemoteConnectionForCppCompilation(cppInFile); remote == nil {
				t.Fatalf("numRemotes=%d %s: got nil remote", numRemotes, cppInFile)
			}
		}
	}
}

// The invariant itself, which is worth checking on whatever architecture the
// suite happens to run on: every configured remote is reachable by some file
// name, and no name selects out of range. This one is not a guard against the
// 32-bit bug above -- it cannot be -- so it does not skip.
func TestChooseRemoteConnectionUsesEveryRemote(t *testing.T) {
	for _, numRemotes := range []int{1, 2, 3, 4, 5, 8} {
		daemon := makeDaemonWithRemotes(numRemotes)

		seen := make(map[string]bool, numRemotes)
		for i := 0; i < 20000; i++ {
			cppInFile := fmt.Sprintf("src/file%d.cpp", i)
			remote := daemon.chooseRemoteConnectionForCppCompilation(cppInFile)
			if remote == nil {
				t.Fatalf("numRemotes=%d %s: got nil remote", numRemotes, cppInFile)
			}
			seen[remote.remoteHost] = true
		}
		if len(seen) != numRemotes {
			t.Errorf("numRemotes=%d: only %d of the remotes were ever chosen", numRemotes, len(seen))
		}
	}
}

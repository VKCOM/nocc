package client

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// A server runs the compiler NAME we send, resolved on ITS PATH. Usually that's the point —
// it's what lets a fast 64-bit machine serve a 32-bit client, as long as both sides agree that,
// say, "arm-linux-gnueabihf-g++" means the cross compiler for this client's target.
//
// But a plain "g++" exists everywhere and means whatever the machine it runs on is, so a
// misconfigured (or merely auto-discovered) server can answer with objects built for its own
// architecture. We tell the server what we're building for, and it refuses the session on
// a mismatch; this is where that answer comes from.
//
// Detection is per compiler name and cached for the daemon's lifetime: it costs one exec,
// and a build invokes the daemon thousands of times.

func (daemon *Daemon) GetCxxTargetTriplet(cxxName string) string {
	daemon.mu.RLock()
	triplet, exists := daemon.cxxTargetTriplets[cxxName]
	daemon.mu.RUnlock()
	if exists {
		return triplet
	}

	triplet = detectCxxTargetTriplet(cxxName)

	daemon.mu.Lock()
	daemon.cxxTargetTriplets[cxxName] = triplet
	daemon.mu.Unlock()

	if triplet != "" {
		logClient.Info(1, "detected local compiler", cxxName, "targeting", triplet)
	}
	return triplet
}

// detectCxxTargetTriplet returns "" if the target can't be determined.
// That's not an error: the server then skips the comparison and serves us as before,
// which is also what happens when the server is an older build that doesn't know about it.
func detectCxxTargetTriplet(cxxName string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, cxxName, "-dumpmachine").Output()
	if err != nil {
		logClient.Info(1, "can't detect the target of", cxxName, err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

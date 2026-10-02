package server

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// A server runs whatever compiler NAME a client sends, resolved on the server's own PATH.
// That's what lets an aarch64 machine serve a 32-bit client: both sides say
// "arm-linux-gnueabihf-g++" and each resolves it locally. It also means a client can reach a
// server where that name means something else entirely — most typically a plain "g++", which
// exists on every machine and targets whatever that machine is.
//
// Left unchecked, such a mismatch has two outcomes, and the quiet one is the dangerous one:
//   - the compilation fails on a system header, and the build stops with an error pointing at
//     glibc rather than at the misconfiguration;
//   - or, for a file that includes no system headers, it SUCCEEDS and returns an object of the
//     wrong architecture. Nothing detects that until the link, far from the cause.
//
// So the compiler is verified once per name, at session start, before anything is uploaded.

type cxxCapability struct {
	targetTriplet string
	err           error
}

// CxxCapabilityCache remembers what each compiler name means on this server.
// Compilers don't change under a running server often enough to justify re-probing per session,
// and a session must not pay for an exec.
type CxxCapabilityCache struct {
	mu     sync.RWMutex
	byName map[string]cxxCapability
}

func MakeCxxCapabilityCache() *CxxCapabilityCache {
	return &CxxCapabilityCache{byName: make(map[string]cxxCapability, 4)}
}

// CheckCompilerMatchesClient returns nil if cxxName exists here and targets clientTriplet.
// An empty clientTriplet means the client couldn't detect its own target (or is an older client):
// then only the compiler's existence is verified, since there's nothing to compare against.
func (cache *CxxCapabilityCache) CheckCompilerMatchesClient(cxxName string, clientTriplet string) error {
	capability := cache.detect(cxxName)
	if capability.err != nil {
		return capability.err
	}

	if clientTriplet != "" && capability.targetTriplet != "" && capability.targetTriplet != clientTriplet {
		return fmt.Errorf("the compiler %q targets %s here, but the client compiles for %s; "+
			"a server can only serve a client whose compiler name resolves to the same target",
			cxxName, capability.targetTriplet, clientTriplet)
	}
	return nil
}

func (cache *CxxCapabilityCache) detect(cxxName string) cxxCapability {
	cache.mu.RLock()
	capability, exists := cache.byName[cxxName]
	cache.mu.RUnlock()
	if exists {
		return capability
	}

	capability = detectCxxCapability(cxxName)

	cache.mu.Lock()
	cache.byName[cxxName] = capability
	cache.mu.Unlock()

	if capability.err != nil {
		logServer.Error("compiler check failed:", capability.err)
	} else {
		logServer.Info(0, "detected compiler", cxxName, "targeting", capability.targetTriplet)
	}
	return capability
}

func detectCxxCapability(cxxName string) cxxCapability {
	cxxPath, err := exec.LookPath(cxxName)
	if err != nil {
		return cxxCapability{err: fmt.Errorf("the compiler %q is not installed on this server", cxxName)}
	}

	// a compiler that hangs on -dumpmachine would otherwise hang every session that asks for it
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, cxxPath, "-dumpmachine").Output()
	if err != nil {
		// not fatal: an exotic compiler may not support -dumpmachine, and it does exist —
		// keep serving it, just without the target check
		logServer.Error("can't detect the target of", cxxName, err)
		return cxxCapability{}
	}
	return cxxCapability{targetTriplet: strings.TrimSpace(string(out))}
}

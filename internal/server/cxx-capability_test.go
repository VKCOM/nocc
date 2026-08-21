package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// the capability check logs its findings; without this, logServer is nil
	if err := MakeLoggerServer("", -1); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// fakeCompiler writes an executable that answers -dumpmachine with the given triplet,
// and puts it on PATH — the same way the real check finds a compiler.
func fakeCompiler(t *testing.T, name string, dumpMachineOutput string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"-dumpmachine\" ]; then echo " + dumpMachineOutput + "; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCompilerMatchingTargetIsAccepted(t *testing.T) {
	fakeCompiler(t, "test-matching-g++", "arm-linux-gnueabihf")

	if err := MakeCxxCapabilityCache().CheckCompilerMatchesClient("test-matching-g++", "arm-linux-gnueabihf"); err != nil {
		t.Errorf("a matching compiler was refused: %v", err)
	}
}

// The case that motivated all of this: a client asks for "g++", the server has a "g++" —
// a different one. Unrefused, it returns objects of the server's architecture, and a build
// that includes no system headers accepts them silently until it links.
func TestCompilerWithForeignTargetIsRefused(t *testing.T) {
	fakeCompiler(t, "test-foreign-g++", "aarch64-linux-gnu")

	err := MakeCxxCapabilityCache().CheckCompilerMatchesClient("test-foreign-g++", "arm-linux-gnueabihf")
	if err == nil {
		t.Fatal("a compiler targeting another architecture was accepted")
	}
	// the message has to name both targets: it's the only clue the user gets
	for _, mustMention := range []string{"aarch64-linux-gnu", "arm-linux-gnueabihf", "test-foreign-g++"} {
		if !strings.Contains(err.Error(), mustMention) {
			t.Errorf("error %q doesn't mention %q", err, mustMention)
		}
	}
}

func TestMissingCompilerIsRefused(t *testing.T) {
	err := MakeCxxCapabilityCache().CheckCompilerMatchesClient("test-not-installed-anywhere-g++", "arm-linux-gnueabihf")
	if err == nil {
		t.Fatal("a compiler that isn't installed was accepted")
	}
}

// An older client sends no triplet. It must keep working: existence is still checked,
// the target comparison simply has nothing to compare against.
func TestClientWithoutTripletIsServed(t *testing.T) {
	fakeCompiler(t, "test-oldclient-g++", "aarch64-linux-gnu")

	if err := MakeCxxCapabilityCache().CheckCompilerMatchesClient("test-oldclient-g++", ""); err != nil {
		t.Errorf("a client that didn't report its target was refused: %v", err)
	}
	if err := MakeCxxCapabilityCache().CheckCompilerMatchesClient("test-not-installed-anywhere-g++", ""); err == nil {
		t.Error("existence must still be checked for a client without a triplet")
	}
}

// A compiler too exotic to answer -dumpmachine is served, not refused: it exists,
// and refusing it would break setups that work today.
func TestCompilerWithoutDumpMachineIsServed(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "test-nodump-g++"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := MakeCxxCapabilityCache().CheckCompilerMatchesClient("test-nodump-g++", "arm-linux-gnueabihf"); err != nil {
		t.Errorf("a compiler without -dumpmachine was refused: %v", err)
	}
}

func TestDetectionIsCachedPerCompilerName(t *testing.T) {
	fakeCompiler(t, "test-cached-g++", "aarch64-linux-gnu")
	cache := MakeCxxCapabilityCache()

	if err := cache.CheckCompilerMatchesClient("test-cached-g++", "aarch64-linux-gnu"); err != nil {
		t.Fatal(err)
	}
	// remove it from PATH entirely: a second check must still answer from the cache,
	// otherwise every session pays for an exec
	t.Setenv("PATH", t.TempDir())
	if err := cache.CheckCompilerMatchesClient("test-cached-g++", "aarch64-linux-gnu"); err != nil {
		t.Errorf("the result wasn't cached: %v", err)
	}
}

package common

import "bytes"

// CompilerWasKilled tells a compiler killed from outside — a server stopping (systemd signals the whole
// cgroup), the OOM killer — from the compiler's verdict on the source. Only the former is worth another try.
//
// A driver killed itself shows as a negative exit code (Go's ExitCode for a signal, or a compiler that
// never started). But gcc's driver survives its cc1plus being killed, and exits 1 just as on a compile
// error; only its message tells. An internal compiler error (a crash, like SIGSEGV) reads differently
// and is deliberately not matched: it would only crash again.
func CompilerWasKilled(exitCode int32, stderr []byte) bool {
	if exitCode < 0 {
		return true
	}
	return exitCode == 1 &&
		(bytes.Contains(stderr, []byte("Killed signal terminated program")) ||
			bytes.Contains(stderr, []byte("Terminated signal terminated program")))
}

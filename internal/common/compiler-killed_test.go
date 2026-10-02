package common

import "testing"

// The gcc messages are verbatim from g++ 14 with its cc1plus sent each signal.
func TestCompilerWasKilled(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int32
		stderr   string
		want     bool
	}{
		{"driver killed by a signal", -1, "signal: terminated", true},
		{"cc1plus SIGKILLed (OOM killer)", 1, "g++: fatal error: Killed signal terminated program cc1plus\ncompilation terminated.", true},
		{"cc1plus SIGTERMed (server stopping)", 1, "g++: fatal error: Terminated signal terminated program cc1plus\ncompilation terminated.", true},
		{"compile error", 1, "main.cpp:3:1: error: expected ';' before '}' token", false},
		{"internal compiler error", 1, "<built-in>: internal compiler error: Segmentation fault", false},
		{"success", 0, "", false},
	}
	for _, c := range cases {
		if got := CompilerWasKilled(c.exitCode, []byte(c.stderr)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

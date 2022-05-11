package client

import (
	"fmt"
	"strings"
	"time"
)

type invocationTimingItem struct {
	stepName string
	timeEnd  time.Time
}

// InvocationSummary represents some meaningful metrics/timings of one `nocc` execution.
// If a log verbosity is greater than 0, this summary is logged as plain text at process finish.
//
// It's mostly for developing/debugging purposes: multiple nocc invocations are appended to a single log file,
// from which we can compute statistics, average and percentiles, either in total or partitioned by hosts.
type InvocationSummary struct {
	remoteHostPort string

	nIncludes      int
	nFilesSent     int
	nBytesSent     int
	nBytesReceived int

	timings []invocationTimingItem
}

func MakeInvocationSummary() *InvocationSummary {
	return &InvocationSummary{
		timings: make([]invocationTimingItem, 0, 4),
	}
}

func (s *InvocationSummary) AddTiming(nameOfDoneStep string) {
	s.timings = append(s.timings, invocationTimingItem{nameOfDoneStep, time.Now()})
}

// ToLogString outputs InvocationSummary in a human-readable and easily parseable string
// that is appended to a specified log file.
func (s *InvocationSummary) ToLogString(invocation *Invocation) string {
	duration := time.Since(invocation.createTime).Milliseconds()

	b := strings.Builder{}
	fmt.Fprintf(&b, "cppInFile=%q, remote=%s, sessionID=%d, nIncludes=%d, nFilesSent=%d, nBytesSent=%d, nBytesReceived=%d, cxxDuration=%dms",
		invocation.cppInFile, s.remoteHostPort, invocation.sessionID, s.nIncludes, s.nFilesSent, s.nBytesSent, s.nBytesReceived, invocation.cxxDuration)

	prevTime := invocation.createTime
	fmt.Fprintf(&b, ", started=0ms")
	for _, item := range s.timings {
		dur := item.timeEnd.Sub(prevTime).Milliseconds()
		fmt.Fprintf(&b, ", %s=+%dms", item.stepName, dur)
		prevTime = item.timeEnd
	}
	fmt.Fprintf(&b, ", total=%dms", duration)

	return b.String()
}

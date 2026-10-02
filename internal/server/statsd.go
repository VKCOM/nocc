package server

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync/atomic"
	"time"
)

// Statsd contains all metrics from server start up till now.
// They are periodically dump to statsd if configured.
type Statsd struct {
	// cumulative statistics, atomics, incremented directly
	// in grafana, to view deltas instead of rising metrics, one should use nonNegativeDerivative
	bytesSent              atomic.Int64
	filesSent              atomic.Int64
	bytesReceived          atomic.Int64
	filesReceived          atomic.Int64
	clientsUnauthenticated atomic.Int64
	sessionsCount          atomic.Int64
	sessionsFailedOpen     atomic.Int64
	sessionsFromObjCache   atomic.Int64
	pchCompilations        atomic.Int64
	pchCompilationsFailed  atomic.Int64

	statsdConnection net.Conn
	statsdBuffer     bytes.Buffer
}

func MakeStatsd(statsdHostPort string) (*Statsd, error) {
	if statsdHostPort == "" {
		return &Statsd{
			statsdConnection: nil,
		}, nil
	}

	conn, err := net.Dial("udp", statsdHostPort)
	if err != nil {
		return nil, err
	}

	return &Statsd{
		statsdConnection: conn,
	}, nil
}

func (cs *Statsd) writeStat(statName string, value int64) {
	fmt.Fprintf(&cs.statsdBuffer, "nocc.%s:%d|g\n", statName, value)
}

func (cs *Statsd) fillBufferWithStats(noccServer *NoccServer) {
	cs.writeStat("server.uptime", int64(time.Since(noccServer.StartTime).Seconds()))
	cs.writeStat("server.goroutines", int64(runtime.NumGoroutine()))

	cs.writeStat("sessions.active", noccServer.ActiveClients.ActiveSessionsCount())
	cs.writeStat("sessions.total", cs.sessionsCount.Load())
	cs.writeStat("sessions.failed_open", cs.sessionsFailedOpen.Load())
	cs.writeStat("sessions.from_obj_cache", cs.sessionsFromObjCache.Load())

	cs.writeStat("clients.active", noccServer.ActiveClients.ActiveCount())
	cs.writeStat("clients.completed", noccServer.ActiveClients.CompletedCount())
	cs.writeStat("clients.files_count", noccServer.ActiveClients.TotalFilesCountInDirs())
	cs.writeStat("clients.unauthenticated", cs.clientsUnauthenticated.Load())

	cs.writeStat("cxx.calls", noccServer.CxxLauncher.GetTotalCxxCallsCount())
	cs.writeStat("cxx.parallel", noccServer.CxxLauncher.GetNowCompilingSessionsCount())
	cs.writeStat("cxx.waiting", noccServer.CxxLauncher.GetWaitingInQueueSessionsCount())
	cs.writeStat("cxx.duration", noccServer.CxxLauncher.GetTotalCxxDurationMilliseconds())
	cs.writeStat("cxx.more10sec", noccServer.CxxLauncher.GetMore10secCount())
	cs.writeStat("cxx.more30sec", noccServer.CxxLauncher.GetMore30secCount())
	cs.writeStat("cxx.nonzero", noccServer.CxxLauncher.GetNonZeroExitCodeCount())

	cs.writeStat("pch.calls", cs.pchCompilations.Load())
	cs.writeStat("pch.failed", cs.pchCompilationsFailed.Load())

	cs.writeStat("send.bytes", cs.bytesSent.Load())
	cs.writeStat("send.files", cs.filesSent.Load())

	cs.writeStat("receive.bytes", cs.bytesReceived.Load())
	cs.writeStat("receive.files", cs.filesReceived.Load())

	cs.writeStat("src_cache.count", noccServer.SrcFileCache.GetFilesCount())
	cs.writeStat("src_cache.purged", noccServer.SrcFileCache.GetPurgedFilesCount())
	cs.writeStat("src_cache.disk_bytes", noccServer.SrcFileCache.GetBytesOnDisk())

	cs.writeStat("obj_cache.count", noccServer.ObjFileCache.GetFilesCount())
	cs.writeStat("obj_cache.purged", noccServer.ObjFileCache.GetPurgedFilesCount())
	cs.writeStat("obj_cache.disk_bytes", noccServer.ObjFileCache.GetBytesOnDisk())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	cs.writeStat("memory.heap_alloc", int64(mem.HeapAlloc))
	cs.writeStat("memory.total_alloc", int64(mem.TotalAlloc))
	cs.writeStat("memory.heap_objects", int64(mem.HeapObjects))

	cs.writeStat("gc.cycles", int64(mem.NumGC))
	cs.writeStat("gc.pause_total", time.Duration(mem.PauseTotalNs).Milliseconds())
}

func (cs *Statsd) SendToStatsd(noccServer *NoccServer) {
	if cs.statsdConnection == nil {
		return
	}

	cs.fillBufferWithStats(noccServer)

	_, err := io.Copy(cs.statsdConnection, &cs.statsdBuffer)
	if err != nil {
		logServer.Error("writing to statsd", err)
	}
	cs.statsdBuffer.Reset()
}

func (cs *Statsd) Close() {
	if cs.statsdConnection != nil {
		_ = cs.statsdConnection.Close()
	}
	cs.statsdConnection = nil
}

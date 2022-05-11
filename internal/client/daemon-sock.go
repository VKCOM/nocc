package client

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// DaemonUnixSockListener is created when `nocc-daemon` starts.
// It listens to a unix socket from `nocc` invocations (from a lightweight C++ wrapper).
// Request/response transferred via this socket are represented as simple C-style strings with \0 delimiters, see below.
type DaemonUnixSockListener struct {
	activeConnections int32
	lastTimeAlive     time.Time
	netListener       net.Listener
}

type DaemonSockRequest struct {
	Cwd     string
	CmdLine []string
}

type DaemonSockResponse struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

func MakeDaemonRpcListener() *DaemonUnixSockListener {
	return &DaemonUnixSockListener{
		activeConnections: 0,
		lastTimeAlive:     time.Now(),
	}
}

func (listener *DaemonUnixSockListener) StartListeningUnixSocket(daemonUnixSock string) (err error) {
	_ = os.Remove(daemonUnixSock)
	listener.netListener, err = net.Listen("unix", daemonUnixSock)
	return
}

func (listener *DaemonUnixSockListener) StartAcceptingConnections(daemon *Daemon) {
	for {
		conn, err := listener.netListener.Accept()
		if err != nil {
			select {
			case <-daemon.quitChan:
				return
			default:
				logClient.Error("daemon accept error:", err)
			}
		} else {
			listener.lastTimeAlive = time.Now()
			go listener.onRequest(conn, daemon) // `nocc` invocation
		}
	}
}

func (listener *DaemonUnixSockListener) EnterInfiniteLoopUntilQuit(daemon *Daemon) {
	for {
		select {
		case <-daemon.quitChan:
			_ = listener.netListener.Close() // Accept() will return an error immediately
			return

		case <-time.After(5 * time.Second):
			nActive := atomic.LoadInt32(&listener.activeConnections)
			if nActive == 0 && time.Since(listener.lastTimeAlive).Seconds() > 15 {
				daemon.QuitDaemonGracefully("no connections receiving anymore")
			}
		}
	}
}

// onRequest parses a string-encoded message from `nocc` C++ client and calls Daemon.HandleInvocation.
// After the request has been fully processed (.o is written), we answer back, and `nocc` client dies.
// Request message format:
// "{Cwd} {CmdLine...}\0"
// Response message format:
// "{ExitCode}\0{Stdout}\0{Stderr}\0"
// See nocc.cpp, write_request_to_go_daemon() and read_response_from_go_daemon()
func (listener *DaemonUnixSockListener) onRequest(conn net.Conn, daemon *Daemon) {
	slice, err := bufio.NewReader(conn).ReadSlice(0)
	if err != nil {
		if err != io.EOF { // if launched `nocc start {cxx_name}`, and the daemon was already running — nothing is sent actually
			logClient.Error("couldn't read from socket", err)
		}
		listener.respondErr(conn)
		return
	}
	reqParts := strings.Split(string(slice[0:len(slice)-1]), "\b") // -1 to strip off the trailing '\0'
	if len(reqParts) < 3 {
		logClient.Error("couldn't read from socket", reqParts)
		listener.respondErr(conn)
		return
	}
	request := DaemonSockRequest{
		Cwd:     reqParts[0],
		CmdLine: reqParts[1:],
	}

	atomic.AddInt32(&listener.activeConnections, 1)
	response := daemon.HandleInvocation(request)
	atomic.AddInt32(&listener.activeConnections, -1)
	listener.lastTimeAlive = time.Now()

	listener.respondOk(conn, &response)
}

func (listener *DaemonUnixSockListener) respondOk(conn net.Conn, resp *DaemonSockResponse) {
	_, _ = conn.Write([]byte(fmt.Sprintf("%d\000%s\000%s\000", resp.ExitCode, resp.Stdout, resp.Stderr)))
	_ = conn.Close()
}

func (listener *DaemonUnixSockListener) respondErr(conn net.Conn) {
	_, _ = conn.Write([]byte("\000"))
	_ = conn.Close()
}

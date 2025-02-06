package client

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/VKCOM/nocc/internal/common"
)

const (
	timeoutForceInterruptInvocation = 8 * time.Minute
)

// Daemon is created once, in a separate process `nocc-daemon`, which is listening for connections via unix socket.
// `nocc-daemon` is created by the first `nocc` invocation.
// `nocc` is invoked from cmake/kphp. It's a lightweight C++ wrapper that pipes command-line invocation to a daemon.
// The daemon keeps grpc connections to all servers and stores includes cache in memory.
// `nocc-daemon` quits in 15 seconds after it stops receiving new connections.
// (the next `nocc` invocation will spawn the daemon again)
type Daemon struct {
	startTime time.Time
	quitChan  chan int

	clientID     string
	hostUserName string

	listener          *DaemonUnixSockListener
	remoteConnections []*RemoteConnection
	allRemotesDelim   string
	localCxxThrottle  chan struct{}

	disableObjCache    bool
	disableOwnIncludes bool
	disableLocalCxx    bool

	totalInvocations  uint32
	activeInvocations map[uint32]*Invocation
	mu                sync.RWMutex

	includesCache map[string]*IncludesCache // map[cxx_name] => cache (support various cxx compilers during a daemon lifetime)
}

// detectClientID returns a clientID for current daemon launch.
// It's either controlled by env NOCC_CLIENT_ID or a random set of chars
// (it means, that after a daemon dies and launches again after some time, it becomes a new client for the server).
func detectClientID() string {
	clientID := os.Getenv("NOCC_CLIENT_ID")
	if clientID != "" {
		return clientID
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

	b := make([]rune, 8)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	return string(b)
}

func detectHostUserName() string {
	curUser, err := user.Current()
	if err != nil {
		return "unknown"
	}
	return curUser.Username
}

func MakeDaemon(remoteNoccHosts []string, disableObjCache bool, disableOwnIncludes bool, maxLocalCxxProcesses int64) (*Daemon, error) {
	// send env NOCC_SERVERS on connect everywhere
	// this is for debugging purpose: in production, all clients should have the same servers list
	// to ensure this, just grep server logs: only one unique string should appear
	allRemotesDelim := ""
	for _, remoteHostPort := range remoteNoccHosts {
		if allRemotesDelim != "" {
			allRemotesDelim += ","
		}
		allRemotesDelim += ExtractRemoteHostWithoutPort(remoteHostPort)
	}

	// env NOCC_SERVERS and others are supposed to be the same between `nocc` invocations
	// (in practice, this is true, as the first `nocc` invocation has no precedence over any other in a bunch)
	daemon := &Daemon{
		startTime:          time.Now(),
		quitChan:           make(chan int),
		clientID:           detectClientID(),
		hostUserName:       detectHostUserName(),
		remoteConnections:  make([]*RemoteConnection, len(remoteNoccHosts)),
		allRemotesDelim:    allRemotesDelim,
		localCxxThrottle:   make(chan struct{}, maxLocalCxxProcesses),
		disableOwnIncludes: disableOwnIncludes,
		disableObjCache:    disableObjCache,
		disableLocalCxx:    maxLocalCxxProcesses == 0,
		activeInvocations:  make(map[uint32]*Invocation, 300),
		includesCache:      make(map[string]*IncludesCache, 1),
	}

	// connect to all remotes in parallel
	wg := sync.WaitGroup{}
	wg.Add(len(remoteNoccHosts))

	ctxConnect, cancelFunc := context.WithTimeout(context.Background(), 5000*time.Millisecond)
	defer cancelFunc()

	for index, remoteHostPort := range remoteNoccHosts {
		go func(index int, remoteHostPort string) {
			remote, err := MakeRemoteConnection(daemon, remoteHostPort, ctxConnect)
			if err != nil {
				remote.isUnavailable = true
				logClient.Error("error connecting to", remoteHostPort, err)
			}

			daemon.remoteConnections[index] = remote
			wg.Done()
		}(index, remoteHostPort)
	}
	wg.Wait()

	return daemon, nil
}

func (daemon *Daemon) StartListeningUnixSocket(daemonUnixSock string) error {
	daemon.listener = MakeDaemonRpcListener()
	return daemon.listener.StartListeningUnixSocket(daemonUnixSock)
}

func (daemon *Daemon) ServeUntilNobodyAlive() {
	logClient.Info(0, "nocc-daemon started in", time.Since(daemon.startTime).Milliseconds(), "ms")

	var rLimit syscall.Rlimit
	_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	logClient.Info(0, "env:", "clientID", daemon.clientID, "; user", daemon.hostUserName, "; num servers", len(daemon.remoteConnections), "; ulimit -n", rLimit.Cur, "; num cpu", runtime.NumCPU(), "; version", common.GetVersion())

	go daemon.PeriodicallyInterruptHangedInvocations()
	go daemon.listener.StartAcceptingConnections(daemon)
	daemon.listener.EnterInfiniteLoopUntilQuit(daemon)
}

func (daemon *Daemon) QuitDaemonGracefully(reason string) {
	logClient.Info(0, "daemon quit:", reason)

	defer func() { _ = recover() }()
	close(daemon.quitChan)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, remote := range daemon.remoteConnections {
		remote.SendStopClient(ctx)
		remote.Clear()
	}

	daemon.mu.Lock()
	for _, invocation := range daemon.activeInvocations {
		invocation.ForceInterrupt(fmt.Errorf("daemon quit: %v", reason))
	}
	daemon.mu.Unlock()
}

func (daemon *Daemon) OnRemoteBecameUnavailable(remoteHostPost string, reason error) {
	for _, remote := range daemon.remoteConnections {
		if remote.remoteHostPort == remoteHostPost && !remote.isUnavailable {
			remote.isUnavailable = true
			logClient.Error("remote", remoteHostPost, "became unavailable:", reason)
		}
	}
}

func (daemon *Daemon) HandleInvocation(req DaemonSockRequest) DaemonSockResponse {
	invocation := ParseCmdLineInvocation(daemon, req.Cwd, req.CmdLine)

	switch invocation.invokeType {
	default:
		return daemon.FallbackToLocalCxx(req, errors.New("unexpected invokeType after parsing"))

	case invokedUnsupported:
		// if command-line has unsupported options or is non-well-formed,
		// invocation.err describes a human-readable reason
		return daemon.FallbackToLocalCxx(req, invocation.err)

	case invokedForLinking:
		// generally, linking commands are detected by the C++ wrapper, they aren't sent to daemon at all
		// (it's a moment of optimization, because linking commands are usually very long)
		// that's why it's rather strange if this case is true in production, but it's not an error anyway
		logClient.Info(1, "fallback to local cxx for linking")
		return daemon.FallbackToLocalCxx(req, nil)

	case invokedForCompilingPch:
		invocation.includesCache.Clear()
		ownPch, err := GenerateOwnPch(daemon, req.Cwd, invocation)
		if err != nil {
			return daemon.FallbackToLocalCxx(req, fmt.Errorf("failed to generate pch file: %v", err))
		}

		fileSize, err := ownPch.SaveToOwnPchFile()
		if err != nil {
			return daemon.FallbackToLocalCxx(req, fmt.Errorf("failed to save pch file: %v", err))
		}

		invocation.includesCache.AddHFileInfo(ownPch.OwnPchFile, fileSize, ownPch.PchHash, []string{})
		logClient.Info(0, "saved pch file", fileSize, "bytes to", ownPch.OwnPchFile)

		if !daemon.areAllRemotesAvailable() {
			logClient.Info(0, "compiling real pch file for future local compilations", invocation.objOutFile)
			return daemon.FallbackToLocalCxx(req, nil)
		}

		return DaemonSockResponse{
			ExitCode: 0,
			Stdout:   []byte(fmt.Sprintf("[nocc] saved pch file to %s\n", ownPch.OwnPchFile)),
		}

	case invokedForCompilingCpp:
		if len(daemon.remoteConnections) == 0 {
			return daemon.FallbackToLocalCxx(req, fmt.Errorf("no remote hosts set; use NOCC_SERVERS env var to provide servers"))
		}

		remote := daemon.chooseRemoteConnectionForCppCompilation(invocation.cppInFile)
		invocation.summary.remoteHost = remote.remoteHost

		if remote.isUnavailable {
			return daemon.FallbackToLocalCxx(req, fmt.Errorf("remote %s is unavailable", remote.remoteHost))
		}

		daemon.mu.Lock()
		daemon.activeInvocations[invocation.sessionID] = invocation
		daemon.mu.Unlock()

		var err error
		var reply DaemonSockResponse
		reply.ExitCode, reply.Stdout, reply.Stderr, err = CompileCppRemotely(daemon, req.Cwd, invocation, remote)

		daemon.mu.Lock()
		delete(daemon.activeInvocations, invocation.sessionID)
		daemon.mu.Unlock()

		if err != nil { // it's not an error in C++ code, it's a network error or remote failure
			return daemon.FallbackToLocalCxx(req, err)
		}

		logClient.Info(1, "summary:", invocation.summary.ToLogString(invocation))
		return reply
	}
}

func (daemon *Daemon) FallbackToLocalCxx(req DaemonSockRequest, reason error) DaemonSockResponse {
	if reason != nil {
		logClient.Error("compiling locally:", reason)
	}

	var reply DaemonSockResponse
	if daemon.disableLocalCxx {
		reply.ExitCode = 1
		reply.Stderr = []byte("fallback to local cxx disabled")
		return reply
	}

	daemon.localCxxThrottle <- struct{}{}
	localCxx := LocalCxxLaunch{req.CmdLine, req.Cwd}
	reply.ExitCode, reply.Stdout, reply.Stderr = localCxx.RunCxxLocally()
	<-daemon.localCxxThrottle

	return reply
}

func (daemon *Daemon) GetOrCreateIncludesCache(cxxName string, cxxArgs []string) *IncludesCache {
	cxxCacheName := cxxName + strings.Join(cxxArgs, " ")

	daemon.mu.Lock()
	includesCache := daemon.includesCache[cxxCacheName]
	if includesCache == nil {
		var err error
		if includesCache, err = MakeIncludesCache(cxxName, cxxArgs); err != nil {
			logClient.Error("failed to calc default include dirs for", cxxName, err)
		}
		daemon.includesCache[cxxCacheName] = includesCache
	}
	daemon.mu.Unlock()
	return includesCache
}

func (daemon *Daemon) FindBySessionID(sessionID uint32) *Invocation {
	daemon.mu.RLock()
	invocation := daemon.activeInvocations[sessionID]
	daemon.mu.RUnlock()
	return invocation
}

func (daemon *Daemon) PeriodicallyInterruptHangedInvocations() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM)

	for {
		select {
		case <-daemon.quitChan:
			return

		case sig := <-signals:
			if sig == syscall.SIGKILL {
				logClient.Info(0, "got sigkill, exit(9)")
				os.Exit(9)
			}
			if sig == syscall.SIGTERM {
				daemon.QuitDaemonGracefully("got sigterm")
			}

		case <-time.After(10 * time.Second):
			daemon.mu.Lock()
			for _, invocation := range daemon.activeInvocations {
				if time.Since(invocation.createTime) > timeoutForceInterruptInvocation {
					invocation.ForceInterrupt(fmt.Errorf("interrupt sessionID %d (%s) after %d sec timeout", invocation.sessionID, invocation.summary.remoteHost, int(time.Since(invocation.createTime).Seconds())))
				}
			}
			daemon.mu.Unlock()
		}
	}
}

func (daemon *Daemon) areAllRemotesAvailable() bool {
	for _, remote := range daemon.remoteConnections {
		if remote.isUnavailable {
			return false
		}
	}
	return true
}

func (daemon *Daemon) chooseRemoteConnectionForCppCompilation(cppInFile string) *RemoteConnection {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(filepath.Base(cppInFile)))
	return daemon.remoteConnections[int(hasher.Sum32())%len(daemon.remoteConnections)]
}

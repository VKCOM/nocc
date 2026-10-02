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
	"sync"
	"syscall"
	"time"

	"github.com/VKCOM/nocc/internal/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	remoteConnections []*RemoteConnection // replaced as a whole (copy-on-write) on reconnect; read via getRemotes
	remotesMu         sync.RWMutex
	reconnectMu       sync.Mutex // one reconnect round at a time, see reconnectUnavailableRemotes
	allRemotesDelim   string
	localCxxThrottle  chan struct{}

	disableObjCache      bool
	disableOwnIncludes   bool
	disableLocalCxx      bool
	disableLocalFallback bool // a .cpp that can't be compiled remotely fails instead of being compiled here
	remoteRetries        int  // how many more times a failed remote compilation is tried before giving up

	totalInvocations  uint32
	activeInvocations map[uint32]*Invocation
	mu                sync.RWMutex

	includesCache     map[string]*IncludesCache // map[cxx_name] => cache (support various cxx compilers during a daemon lifetime)
	cxxTargetTriplets map[string]string         // map[cxx_name] => `cxx -dumpmachine`, see GetCxxTargetTriplet

	forceInterruptTimeout time.Duration
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

func MakeDaemon(remoteNoccHosts []string, disableObjCache bool, disableOwnIncludes bool, maxLocalCxxProcesses int64, forceInterruptTimeout time.Duration, remoteRetries int64, disableLocalFallback bool) (*Daemon, error) {
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
		startTime:             time.Now(),
		quitChan:              make(chan int),
		clientID:              detectClientID(),
		hostUserName:          detectHostUserName(),
		remoteConnections:     make([]*RemoteConnection, len(remoteNoccHosts)),
		allRemotesDelim:       allRemotesDelim,
		localCxxThrottle:      make(chan struct{}, maxLocalCxxProcesses),
		disableOwnIncludes:    disableOwnIncludes,
		disableObjCache:       disableObjCache,
		disableLocalCxx:       maxLocalCxxProcesses == 0,
		disableLocalFallback:  disableLocalFallback,
		remoteRetries:         int(remoteRetries),
		activeInvocations:     make(map[uint32]*Invocation, 300),
		includesCache:         make(map[string]*IncludesCache, 1),
		cxxTargetTriplets:     make(map[string]string, 1),
		forceInterruptTimeout: forceInterruptTimeout,
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
				remote.isUnavailable.Store(true)
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
	logClient.Info(0, "env:", "clientID", daemon.clientID, "; user", daemon.hostUserName, "; num servers", len(daemon.getRemotes()), "; retries", daemon.remoteRetries, "; local fallback", !daemon.disableLocalFallback, "; ulimit -n", rLimit.Cur, "; num cpu", runtime.NumCPU(), "; version", common.GetVersion())

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
	for _, remote := range daemon.getRemotes() {
		remote.SendStopClient(ctx)
		remote.Clear()
	}

	daemon.mu.Lock()
	for _, invocation := range daemon.activeInvocations {
		invocation.ForceInterrupt(fmt.Errorf("daemon quit: %v", reason))
	}
	daemon.mu.Unlock()
}

// OnRemoteBecameUnavailable is called by a stream of grpcClient that broke and couldn't be reopened.
// The connection is matched by identity, not by host: a stream of a connection already replaced
// by a reconnect must not take down its successor to the same host.
func (daemon *Daemon) OnRemoteBecameUnavailable(grpcClient *GRPCClient, reason error) {
	for _, remote := range daemon.getRemotes() {
		if remote.grpcClient == grpcClient && remote.isUnavailable.CompareAndSwap(false, true) {
			logClient.Error("remote", remote.remoteHostPort, "became unavailable:", reason)
			daemon.interruptInvocationsOnRemote(remote, fmt.Errorf("remote %s became unavailable: %v", remote.remoteHost, reason))
		}
	}
}

// interruptInvocationsOnRemote fails compilations in flight on a remote that went down.
// Their uploads or .o files will never arrive, and waiting for forceInterruptTimeout
// before retrying elsewhere (or falling back) would stall the build for minutes.
func (daemon *Daemon) interruptInvocationsOnRemote(remote *RemoteConnection, reason error) {
	daemon.mu.RLock()
	defer daemon.mu.RUnlock()
	for _, invocation := range daemon.activeInvocations {
		if invocation.remote == remote {
			invocation.ForceInterrupt(reason)
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

		if !daemon.areAllRemotesAvailable() && !daemon.disableLocalFallback {
			logClient.Info(0, "compiling real pch file for future local compilations", invocation.objOutFile)
			return daemon.FallbackToLocalCxx(req, nil)
		}

		return DaemonSockResponse{
			ExitCode: 0,
			Stdout:   []byte(fmt.Sprintf("[nocc] saved pch file to %s\n", ownPch.OwnPchFile)),
		}

	case invokedForCompilingCpp:
		return daemon.compileCppRemotelyWithRetries(req, invocation)
	}
}

// compileCppRemotelyWithRetries compiles a .cpp on the pool of servers, retrying up to remoteRetries times
// after a network or server failure. Every retry gives servers marked unavailable another chance first
// (a restarted server is usable again), then picks a server the usual way.
// Only after all retries fail does it fall back to local compilation, or fail when that's disabled.
func (daemon *Daemon) compileCppRemotelyWithRetries(req DaemonSockRequest, invocation *Invocation) DaemonSockResponse {
	if len(daemon.getRemotes()) == 0 {
		return daemon.onRemoteCompilationFailed(req, invocation, fmt.Errorf("no remote hosts set; use NOCC_SERVERS env var to provide servers"))
	}

	for attempt := 0; ; attempt++ {
		reply, err := daemon.tryCompileCppRemotely(req, invocation)
		if err == nil {
			return reply
		}
		if attempt >= daemon.remoteRetries {
			return daemon.onRemoteCompilationFailed(req, invocation, err)
		}

		delay := remoteRetryDelay(attempt)
		logClient.Error("remote compilation failed, retry", attempt+1, "of", daemon.remoteRetries, "in", delay, invocation.cppInFile+":", err)
		time.Sleep(delay)
		daemon.reconnectUnavailableRemotes()

		// a fresh sessionID: the failed session may still be alive on a server, and must not be confused with this one
		invocation = ParseCmdLineInvocation(daemon, req.Cwd, req.CmdLine)
	}
}

// remoteRetryDelay is 1s, 2s, 4s, 8s, then 10s for every next retry — long enough in total for a server
// to come back from a restart, short enough not to stall a build that only hit a blip.
func remoteRetryDelay(attempt int) time.Duration {
	if attempt >= 4 {
		return 10 * time.Second
	}
	return time.Second << attempt
}

// tryCompileCppRemotely is one attempt to compile a .cpp on some server.
// A non-nil error means a network or server failure, not an error in C++ code (that's a non-zero ExitCode).
func (daemon *Daemon) tryCompileCppRemotely(req DaemonSockRequest, invocation *Invocation) (DaemonSockResponse, error) {
	remote := daemon.chooseRemoteConnectionForCppCompilation(invocation.cppInFile, invocation.cxxName)
	if remote == nil {
		return DaemonSockResponse{}, fmt.Errorf("no remote can compile %s with %s", invocation.cppInFile, invocation.cxxName)
	}
	invocation.remote = remote
	invocation.summary.remoteHost = remote.remoteHost

	daemon.mu.Lock()
	daemon.activeInvocations[invocation.sessionID] = invocation
	daemon.mu.Unlock()

	var err error
	var reply DaemonSockResponse
	reply.ExitCode, reply.Stdout, reply.Stderr, err = CompileCppRemotely(daemon, req.Cwd, invocation, remote)

	daemon.mu.Lock()
	delete(daemon.activeInvocations, invocation.sessionID)
	daemon.mu.Unlock()

	if err != nil {
		// a remote that refused to be this compiler stays refused: the next file hashing here
		// goes elsewhere, so a mismatched server costs one local compilation, not one per file
		if status.Code(err) == codes.FailedPrecondition {
			remote.MarkIncapableOfCxx(invocation.cxxName, status.Convert(err).Message())
		}
		return reply, err
	}

	logClient.Info(1, "summary:", invocation.summary.ToLogString(invocation))
	return reply, nil
}

// onRemoteCompilationFailed is the end of the road for a .cpp no server could compile.
func (daemon *Daemon) onRemoteCompilationFailed(req DaemonSockRequest, invocation *Invocation, reason error) DaemonSockResponse {
	if !daemon.disableLocalFallback {
		return daemon.FallbackToLocalCxx(req, reason)
	}

	logClient.Error("remote compilation failed, local fallback disabled:", invocation.cppInFile, reason)
	return DaemonSockResponse{
		ExitCode: 1,
		Stderr:   []byte(fmt.Sprintf("[nocc] could not compile %s remotely (%d retries): %v; not compiling locally, since NOCC_DISABLE_LOCAL_FALLBACK is set\n", invocation.cppInFile, daemon.remoteRetries, reason)),
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

func (daemon *Daemon) GetOrCreateIncludesCache(cxxName string) *IncludesCache {
	daemon.mu.Lock()
	includesCache := daemon.includesCache[cxxName]
	if includesCache == nil {
		var err error
		if includesCache, err = MakeIncludesCache(cxxName); err != nil {
			logClient.Error("failed to calc default include dirs for", cxxName, err)
		}
		daemon.includesCache[cxxName] = includesCache
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
				if time.Since(invocation.createTime) > daemon.forceInterruptTimeout {
					invocation.ForceInterrupt(fmt.Errorf("interrupt sessionID %d (%s) after %d sec timeout", invocation.sessionID, invocation.summary.remoteHost, int(time.Since(invocation.createTime).Seconds())))
				}
			}
			daemon.mu.Unlock()
		}
	}
}

func (daemon *Daemon) getRemotes() []*RemoteConnection {
	daemon.remotesMu.RLock()
	defer daemon.remotesMu.RUnlock()
	return daemon.remoteConnections
}

// reconnectUnavailableRemotes gives every remote marked unavailable another chance.
// Nothing else ever clears isUnavailable, so without this, a server that restarted (or a network
// that blipped) mid-build would stay unused for the rest of it. A reconnect builds a whole new
// RemoteConnection rather than reviving the old one: in-flight state of the old one (its upload
// queue, its streams) is abandoned along with it.
// Attempts to one host are throttled, so a pool of retrying invocations doesn't hammer a host that's really down.
func (daemon *Daemon) reconnectUnavailableRemotes() {
	const minReconnectInterval = time.Second

	daemon.reconnectMu.Lock()
	defer daemon.reconnectMu.Unlock()

	remotes := daemon.getRemotes()
	updated := make([]*RemoteConnection, len(remotes))
	copy(updated, remotes)

	for index, old := range remotes {
		if !old.isUnavailable.Load() || time.Since(old.createTime) < minReconnectInterval {
			continue
		}

		ctxConnect, cancelFunc := context.WithTimeout(context.Background(), 3*time.Second)
		remote, err := MakeRemoteConnection(daemon, old.remoteHostPort, ctxConnect)
		cancelFunc()
		if err != nil {
			remote.isUnavailable.Store(true)
			logClient.Error("reconnect to", old.remoteHostPort, "failed:", err)
		} else {
			logClient.Info(0, "reconnected to", old.remoteHostPort)
		}

		updated[index] = remote
		old.Clear()
	}

	daemon.remotesMu.Lock()
	daemon.remoteConnections = updated
	daemon.remotesMu.Unlock()
}

func (daemon *Daemon) areAllRemotesAvailable() bool {
	for _, remote := range daemon.getRemotes() {
		if remote.isUnavailable.Load() {
			return false
		}
	}
	return true
}

// chooseRemoteConnectionForCppCompilation picks the server a .cpp is compiled on.
// The choice is a hash of the file's basename, so the same file goes to the same server from
// every machine and finds its dependencies already uploaded there.
//
// If that server can't take the file — it's down, or it isn't the compiler we asked for — we walk
// forward to the next one that can, instead of compiling locally. Only the files that hashed to the
// unusable server move, and they move deterministically, so the rest of the mapping (and every other
// server's cache) is untouched. Compiling locally is the last resort: on the slow, often single-core
// machines nocc exists for, "1/N of the build runs here" is the outcome worth avoiding.
//
// Returns nil if no server can serve this compilation at all.
func (daemon *Daemon) chooseRemoteConnectionForCppCompilation(cppInFile string, cxxName string) *RemoteConnection {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(filepath.Base(cppInFile)))
	// Reduce in uint32, not int: on a 32-bit client (armv7 boards) int is 32 bits, so
	// int(hasher.Sum32()) is negative for about half of all file names, and Go's % keeps
	// the sign of the dividend — which would index out of range below.
	remotes := daemon.getRemotes()
	nRemotes := uint32(len(remotes))
	startIndex := hasher.Sum32() % nRemotes

	for offset := uint32(0); offset < nRemotes; offset++ {
		remote := remotes[(startIndex+offset)%nRemotes]
		if remote.CanCompileWithCxx(cxxName) {
			return remote
		}
	}
	return nil
}

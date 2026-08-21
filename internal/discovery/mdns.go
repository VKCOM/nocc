// Package discovery implements optional zeroconf (mDNS/DNS-SD) discovery of nocc servers.
//
// A nocc-server may advertise itself on the local network as `_nocc._tcp.local.`, and
// a nocc-daemon may browse for such advertisements instead of (or in addition to) the
// static NOCC_SERVERS list. It's the same idea as distcc's `_distcc._tcp` zeroconf mode:
// on a small LAN of build machines, nobody has to maintain a host list by hand.
//
// Both sides are opt-in and off by default: a server doesn't advertise unless asked to,
// and a daemon doesn't accept discovered servers unless asked to. Compilation ships
// source code to whoever answers, so joining a build cluster stays an explicit decision.
package discovery

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/libp2p/zeroconf/v2"
)

const (
	// ServiceType is the DNS-SD service type nocc servers advertise under.
	ServiceType = "_nocc._tcp"
	// ServiceDomain is the DNS-SD domain; mDNS is always "local.".
	ServiceDomain = "local."

	// txtVersion is bumped if the meaning of TXT keys ever changes incompatibly.
	txtVersion = "1"
)

// ServerInfo is one discovered nocc-server: an address to connect to plus whatever
// it told us about itself in TXT records (all TXT fields are advisory, never trusted).
type ServerInfo struct {
	HostPort string // "ip:port", ready for MakeRemoteConnection
	Instance string // advertised instance name, usually the server's hostname
	Version  string // remote's nocc version, for logging and mismatch warnings
	NumCPU   int    // remote's CPU count, 0 if not advertised
	GOARCH   string // remote's architecture, "" if not advertised
	GOOS     string // remote's OS, "" if not advertised
}

// Advertiser is a running mDNS advertisement; call Shutdown to withdraw it.
type Advertiser struct {
	server *zeroconf.Server
}

// Advertise publishes this nocc-server as `_nocc._tcp.local.` on every multicast interface.
// instanceName defaults to the machine's hostname when empty.
func Advertise(instanceName string, port int) (*Advertiser, error) {
	if instanceName == "" {
		hostName, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("can't detect hostname for the mdns instance name: %v", err)
		}
		// strip a trailing domain, "build-01.lan" -> "build-01": DNS-SD instance names are labels
		instanceName = strings.SplitN(hostName, ".", 2)[0]
	}

	txt := []string{
		"txtvers=" + txtVersion,
		"v=" + shortVersion(),
		"cpus=" + strconv.Itoa(runtime.NumCPU()),
		"arch=" + runtime.GOARCH,
		"os=" + runtime.GOOS,
	}

	zServer, err := zeroconf.Register(instanceName, ServiceType, ServiceDomain, port, txt, nil)
	if err != nil {
		return nil, err
	}
	return &Advertiser{server: zServer}, nil
}

// shortVersion is "v1.2.2" out of "v1.2.2, rev abcdef, compiled at ...":
// the release is what a client can meaningfully compare, the rest is noise in a TXT record.
func shortVersion() string {
	return strings.TrimSpace(strings.SplitN(common.GetVersion(), ",", 2)[0])
}

func (a *Advertiser) Shutdown() {
	if a != nil && a.server != nil {
		a.server.Shutdown()
	}
}

// Browse collects nocc servers announcing themselves on the LAN, waiting up to timeout.
// It always waits the full timeout: mDNS has no "that's everyone" signal, and stopping at
// the first reply would systematically favour the fastest responder.
// The result is sorted by HostPort, which matters: the daemon shards .cpp files across
// servers by index (fnv(basename) % len(servers)), so an unstable order would send the
// same file to a different server on every machine and defeat the remote src caches.
func Browse(timeout time.Duration) ([]ServerInfo, error) {
	entries := make(chan *zeroconf.ServiceEntry, 16)
	found := make([]ServerInfo, 0, 8)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for entry := range entries {
			if info, ok := serviceEntryToServerInfo(entry); ok {
				found = append(found, info)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// zeroconf.Browse closes `entries` itself once ctx expires
	if err := zeroconf.Browse(ctx, ServiceType, ServiceDomain, entries); err != nil {
		return nil, err
	}
	<-done

	sort.Slice(found, func(i, j int) bool { return found[i].HostPort < found[j].HostPort })
	return dedupSameHostPort(found), nil
}

// serviceEntryToServerInfo converts a raw mDNS reply into a dialable server.
// Entries without a usable address are dropped rather than reported as errors:
// an unrelated or half-broken responder on the LAN shouldn't fail the whole build.
func serviceEntryToServerInfo(entry *zeroconf.ServiceEntry) (ServerInfo, bool) {
	if entry == nil || entry.Port == 0 {
		return ServerInfo{}, false
	}

	host := pickAddress(entry)
	if host == "" && entry.HostName != "" {
		host = strings.TrimSuffix(entry.HostName, ".")
	}
	if host == "" {
		return ServerInfo{}, false
	}

	info := ServerInfo{
		HostPort: net.JoinHostPort(host, strconv.Itoa(entry.Port)),
		Instance: entry.Instance,
	}
	for _, kv := range entry.Text {
		key, value, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		switch key {
		case "v":
			info.Version = value
		case "cpus":
			info.NumCPU, _ = strconv.Atoi(value)
		case "arch":
			info.GOARCH = value
		case "os":
			info.GOOS = value
		}
	}
	return info, true
}

// pickAddress chooses one address out of an announcement.
//
// A server with several interfaces announces several addresses, and two properties matter:
//
//   - reachability: a machine on two subnets announces both, and only one of them is routable
//     from here. Preferring an address sharing a subnet with one of our own interfaces picks
//     the side of the network we're actually on.
//   - determinism: mDNS record order varies between responses, so picking "the first one"
//     would silently alternate between a server's addresses across daemon restarts. Since the
//     daemon shards .cpp files by server index, that alternation reshuffles the whole mapping
//     and throws away the remote src caches. Sorting makes the choice repeatable.
//
// A literal address is preferred over the announced hostname because the client may have no
// mDNS resolver even when it can receive mDNS (containers and minimal images typically don't).
func pickAddress(entry *zeroconf.ServiceEntry) string {
	usable := make([]net.IP, 0, len(entry.AddrIPv4)+len(entry.AddrIPv6))
	for _, ip := range entry.AddrIPv4 {
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsUnspecified() && !ip4.IsLoopback() {
			usable = append(usable, ip4)
		}
	}
	for _, ip := range entry.AddrIPv6 {
		if ip.To4() == nil && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			usable = append(usable, ip)
		}
	}
	if len(usable) == 0 {
		return ""
	}

	sort.Slice(usable, func(i, j int) bool {
		iIsV4, jIsV4 := usable[i].To4() != nil, usable[j].To4() != nil
		if iIsV4 != jIsV4 { // IPv4 first: it's what nocc setups are actually routed over
			return iIsV4
		}
		return bytes.Compare(usable[i], usable[j]) < 0
	})

	localNets := localSubnets()
	for _, ip := range usable {
		for _, ipNet := range localNets {
			if ipNet.Contains(ip) {
				return ip.String()
			}
		}
	}
	return usable[0].String()
}

// localSubnets returns the networks this machine has an address on.
// Errors are swallowed: without this list we fall back to a deterministic choice,
// which is worse but never wrong enough to matter.
func localSubnets() []*net.IPNet {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	nets := make([]*net.IPNet, 0, len(addrs))
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			nets = append(nets, ipNet)
		}
	}
	return nets
}

// dedupSameHostPort drops repeated announcements of one server.
// A server visible on several interfaces (or re-announcing during our browse window) arrives
// more than once, sometimes under a different address of the same machine. A duplicate isn't a
// connection error — it's one machine occupying two slots of the sharding wheel and receiving a
// double share of the build, so identity is matched by instance name as well as by address.
func dedupSameHostPort(sortedFound []ServerInfo) []ServerInfo {
	unique := sortedFound[:0]
	seen := make(map[string]bool, len(sortedFound)*2)
	for _, info := range sortedFound {
		_, port, _ := net.SplitHostPort(info.HostPort)
		instanceKey := "instance:" + strings.ToLower(info.Instance) + ":" + port
		if seen[info.HostPort] || (info.Instance != "" && seen[instanceKey]) {
			continue
		}
		seen[info.HostPort] = true
		seen[instanceKey] = true
		unique = append(unique, info)
	}
	return unique
}

func (info ServerInfo) String() string {
	descr := info.HostPort
	if info.Instance != "" {
		descr += " (" + info.Instance
		if info.NumCPU > 0 {
			descr += ", " + strconv.Itoa(info.NumCPU) + " cpu"
		}
		if info.GOARCH != "" {
			descr += ", " + info.GOARCH
		}
		descr += ")"
	}
	return descr
}

package discovery

import (
	"context"
	"net"
	"strings"
	"time"
)

// A static host that doesn't resolve (a decommissioned machine still listed in NOCC_SERVERS,
// or DNS being slow) must not add seconds to every daemon start just to answer a dedup question.
// Failing the lookup only risks keeping a duplicate, so give it a short deadline and move on.
const resolveTimeout = 300 * time.Millisecond

// MergeWithStaticHosts appends discovered servers to an explicitly configured list.
//
// The static list always wins: whatever the user wrote in NOCC_SERVERS keeps its exact
// spelling and its position, so enabling discovery can never re-shard an existing setup —
// it can only add servers after the ones already there.
//
// A discovered server that is already in the static list is dropped. That check has to look
// past spelling, because the two sources naturally disagree: NOCC_SERVERS is usually written
// with hostnames ("build-01:43210") while mDNS reports addresses ("10.0.0.5:43210"). A missed
// duplicate isn't a connection error — it's one machine occupying two slots of the sharding
// wheel and receiving a double share of the build.
func MergeWithStaticHosts(staticHosts []string, discovered []ServerInfo) []string {
	merged := make([]string, 0, len(staticHosts)+len(discovered))
	merged = append(merged, staticHosts...)

	staticKeys := make(map[string]bool, len(staticHosts)*2)
	for _, hostPort := range staticHosts {
		for _, key := range hostPortKeys(hostPort) {
			staticKeys[key] = true
		}
	}

	for _, info := range discovered {
		isDuplicate := false
		for _, key := range hostPortKeys(info.HostPort) {
			if staticKeys[key] {
				isDuplicate = true
				break
			}
		}
		// the instance name is the remote's own hostname, which is very often exactly
		// how it's spelled in NOCC_SERVERS — catch that without a DNS round trip
		if !isDuplicate && info.Instance != "" {
			for _, port := range portsOf(info.HostPort) {
				if staticKeys[strings.ToLower(info.Instance)+":"+port] {
					isDuplicate = true
					break
				}
			}
		}
		if !isDuplicate {
			merged = append(merged, info.HostPort)
		}
	}
	return merged
}

// hostPortKeys returns comparable identities of one "host:port" entry:
// the entry as written (lowercased), and one per IP the host resolves to.
// Resolution failures are ignored — an unresolvable static host is a problem for the
// connection attempt to report, not a reason to refuse merging.
func hostPortKeys(hostPort string) []string {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return []string{strings.ToLower(hostPort)}
	}

	keys := []string{strings.ToLower(host) + ":" + port}
	// a bare ".local" name and its short form denote the same machine on a zeroconf LAN
	if shortHost := strings.TrimSuffix(strings.ToLower(host), ".local"); shortHost != strings.ToLower(host) {
		keys = append(keys, shortHost+":"+port)
	}

	if net.ParseIP(host) == nil {
		ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
		defer cancel()
		if addrs, err := net.DefaultResolver.LookupHost(ctx, host); err == nil {
			for _, addr := range addrs {
				keys = append(keys, addr+":"+port)
			}
		}
	}
	return keys
}

func portsOf(hostPort string) []string {
	if _, port, err := net.SplitHostPort(hostPort); err == nil {
		return []string{port}
	}
	return nil
}

package common

import "time"

// Keepalive settings shared by the client dial options and the server's gRPC options.
// They are here, rather than inline at each site, because the two sides have to stay
// compatible: if the client pings more often than the server's EnforcementPolicy
// permits, the server answers with a GOAWAY "too_many_pings" and resets the stream --
// which is the very failure keepalive is being enabled to prevent.
//
// The point of enabling this at all is that a router / NAT / conntrack table between
// client and server can silently evict an idle stream. The eviction surfaces on next
// use as "connection reset by peer", after which the daemon marks that remote
// unavailable and drops the rest of the build to local compilation.
const (
	// KeepaliveTime is how long a connection sits idle before either side pings.
	// Both sides use it. It must stay above KeepaliveMinTime -- see below.
	KeepaliveTime = 20 * time.Second

	// KeepaliveTimeout is how long to wait for a ping ack before considering the
	// connection dead.
	KeepaliveTimeout = 10 * time.Second

	// KeepaliveMinTime is the most frequent client ping the server will tolerate
	// (grpc.KeepaliveEnforcementPolicy). It is deliberately *below* KeepaliveTime
	// rather than equal to it: the server measures the interval it actually observes,
	// so scheduling jitter, a loaded client, or network delay can make a ping sent on
	// a 20s timer arrive fractionally early. Matching the two exactly would leave no
	// margin and turn ordinary jitter into a too_many_pings GOAWAY. Half the client
	// interval is a conservative margin; the cost of the slack is only that a
	// misbehaving client could ping twice as often as ours does, which is negligible
	// traffic.
	KeepaliveMinTime = 10 * time.Second
)

// KeepalivePermitWithoutStream allows pings while no RPC is in flight, on both sides.
// This is the setting that actually matters here: on a slow client the connection sits
// idle *between* files, with no active RPC, which is exactly when a NAT table drops it.
const KeepalivePermitWithoutStream = true

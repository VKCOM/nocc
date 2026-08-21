package discovery

import (
	"reflect"
	"testing"
)

func TestMergeKeepsStaticOrderAndAppends(t *testing.T) {
	// static entries must keep their exact spelling and position: the daemon shards
	// .cpp files by index, so reordering them re-shards an existing setup
	got := MergeWithStaticHosts(
		[]string{"build-01:43210", "build-02:43210"},
		[]ServerInfo{{HostPort: "10.0.0.9:43210", Instance: "build-09"}},
	)
	want := []string{"build-01:43210", "build-02:43210", "10.0.0.9:43210"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeDropsDiscoveredDuplicateByInstanceName(t *testing.T) {
	// NOCC_SERVERS is written with hostnames, mdns answers with IPs: the same machine
	// under two spellings would take a double share of the build
	got := MergeWithStaticHosts(
		[]string{"build-01:43210"},
		[]ServerInfo{
			{HostPort: "10.0.0.1:43210", Instance: "build-01"},
			{HostPort: "10.0.0.2:43210", Instance: "build-02"},
		},
	)
	want := []string{"build-01:43210", "10.0.0.2:43210"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeDropsDiscoveredDuplicateByLiteralMatch(t *testing.T) {
	got := MergeWithStaticHosts(
		[]string{"10.0.0.1:43210"},
		[]ServerInfo{{HostPort: "10.0.0.1:43210", Instance: "build-01"}},
	)
	want := []string{"10.0.0.1:43210"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeTreatsDotLocalAsSameHost(t *testing.T) {
	got := MergeWithStaticHosts(
		[]string{"build-01.local:43210"},
		[]ServerInfo{{HostPort: "10.0.0.1:43210", Instance: "build-01"}},
	)
	want := []string{"build-01.local:43210"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeDifferentPortsAreDifferentServers(t *testing.T) {
	// two nocc-servers on one machine is a legitimate setup; don't collapse them
	got := MergeWithStaticHosts(
		[]string{"build-01:43210"},
		[]ServerInfo{{HostPort: "10.0.0.1:43211", Instance: "build-01"}},
	)
	want := []string{"build-01:43210", "10.0.0.1:43211"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMergeWithNoStaticHosts(t *testing.T) {
	got := MergeWithStaticHosts(nil, []ServerInfo{{HostPort: "10.0.0.1:43210", Instance: "build-01"}})
	want := []string{"10.0.0.1:43210"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

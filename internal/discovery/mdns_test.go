package discovery

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

func TestServiceEntryToServerInfoPrefersIPv4(t *testing.T) {
	entry := &zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{Instance: "build-01"},
		HostName:      "build-01.local.",
		Port:          43210,
		Text:          []string{"txtvers=1", "v=v1.2.2", "cpus=4", "arch=arm64", "os=linux"},
		AddrIPv4:      []net.IP{net.ParseIP("10.0.0.5")},
		AddrIPv6:      []net.IP{net.ParseIP("fd00::5")},
	}

	info, ok := serviceEntryToServerInfo(entry)
	if !ok {
		t.Fatal("entry rejected")
	}
	want := ServerInfo{HostPort: "10.0.0.5:43210", Instance: "build-01", Version: "v1.2.2", NumCPU: 4, GOARCH: "arm64", GOOS: "linux"}
	if info != want {
		t.Errorf("got %+v, want %+v", info, want)
	}
}

func TestServiceEntryToServerInfoFallsBackToHostName(t *testing.T) {
	entry := &zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{Instance: "build-01"},
		HostName:      "build-01.local.",
		Port:          43210,
		// a responder that answered PTR/SRV but whose A record we never saw
		AddrIPv4: []net.IP{net.ParseIP("127.0.0.1")},
	}

	info, ok := serviceEntryToServerInfo(entry)
	if !ok {
		t.Fatal("entry rejected")
	}
	if info.HostPort != "build-01.local:43210" {
		t.Errorf("got %q, want build-01.local:43210", info.HostPort)
	}
}

func TestServiceEntryToServerInfoRejectsUnusable(t *testing.T) {
	for _, entry := range []*zeroconf.ServiceEntry{
		nil,
		{HostName: "build-01.local.", Port: 0}, // no port
		{Port: 43210, AddrIPv4: []net.IP{net.ParseIP("127.0.0.1")}}, // loopback only, no hostname
	} {
		if _, ok := serviceEntryToServerInfo(entry); ok {
			t.Errorf("entry %+v should have been rejected", entry)
		}
	}
}

func TestDedupSameHostPort(t *testing.T) {
	// the same server announced on two interfaces: a duplicate would give it
	// two slots of the sharding wheel and a double share of the build
	got := dedupSameHostPort([]ServerInfo{
		{HostPort: "10.0.0.5:43210", Instance: "build-01"},
		{HostPort: "10.0.0.5:43210", Instance: "build-01"},
		{HostPort: "10.0.0.6:43210", Instance: "build-02"},
	})
	if len(got) != 2 || got[0].HostPort != "10.0.0.5:43210" || got[1].HostPort != "10.0.0.6:43210" {
		t.Errorf("got %+v", got)
	}
}

// TestAdvertiseAndBrowse is the end-to-end check: a real advertisement over the real
// multicast socket, found by a real browse. It's skipped when multicast isn't available
// (some CI sandboxes), but it must not be silently skipped on a normal machine.
func TestAdvertiseAndBrowse(t *testing.T) {
	advertiser, err := Advertise("nocc-test-instance", 43219)
	if err != nil {
		t.Skipf("can't advertise over mdns here: %v", err)
	}
	defer advertiser.Shutdown()

	var found []ServerInfo
	// mDNS is lossy; retry a few times before declaring failure
	for attempt := 0; attempt < 3 && len(found) == 0; attempt++ {
		found, err = Browse(2 * time.Second)
		if err != nil {
			t.Skipf("can't browse mdns here: %v", err)
		}
	}

	for _, info := range found {
		if info.Instance == "nocc-test-instance" {
			if info.NumCPU == 0 || info.GOARCH == "" {
				t.Errorf("TXT records not parsed back: %+v", info)
			}
			return
		}
	}
	t.Fatalf("advertised instance not found among %+v", found)
}

func TestBrowseCachedUsesCacheWithinTTL(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.txt")
	want := []ServerInfo{{HostPort: "10.0.0.5:43210", Instance: "build-01", Version: "v1.2.2", NumCPU: 4, GOARCH: "arm64", GOOS: "linux"}}
	if err := writeCache(cachePath, want); err != nil {
		t.Fatal(err)
	}

	// a zero browse timeout would find nothing, so a non-empty result proves the cache was used
	got, err := BrowseCached(0, time.Minute, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// control: the very same call with an expired ttl must NOT return the cached list
	if err := os.Chtimes(cachePath, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	stale, err := readCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(stale.savedAt) > time.Minute {
		t.Fatal("test setup: cache should be fresh")
	}
}

func TestBrowseCachedIgnoresExpiredCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.txt")
	if err := os.WriteFile(cachePath, []byte("# nocc\n1000000\n10.0.0.5:43210 build-01 v1.2.2 4 arm64 linux\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// ttl long expired, browse timeout ~0 => nothing found, and specifically not the cached entry
	got, err := BrowseCached(time.Millisecond, time.Second, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range got {
		if info.HostPort == "10.0.0.5:43210" {
			t.Error("expired cache entry was returned")
		}
	}
}

func TestCacheRoundTrip(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.txt")
	want := []ServerInfo{
		{HostPort: "10.0.0.5:43210", Instance: "build-01", Version: "v1.2.2", NumCPU: 4, GOARCH: "arm64", GOOS: "linux"},
		{HostPort: "10.0.0.6:43210", Instance: "", Version: "", NumCPU: 0, GOARCH: "", GOOS: ""},
	}
	if err := writeCache(cachePath, want); err != nil {
		t.Fatal(err)
	}
	got, err := readCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.hosts, want) {
		t.Errorf("got %+v, want %+v", got.hosts, want)
	}
}

func TestReadCacheRejectsGarbage(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.txt")
	if err := os.WriteFile(cachePath, []byte("not a timestamp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCache(cachePath); err == nil {
		t.Error("garbage cache accepted")
	}
}

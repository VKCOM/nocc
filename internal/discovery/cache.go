package discovery

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// The daemon exits ~15 seconds after the last `nocc` invocation, so a build that pauses
// (or a handful of one-off compilations) re-runs discovery over and over, paying the full
// browse timeout each time. Caching the last result for a short while removes that cost.
// The window is deliberately short: a stale entry costs a failed connection and a local
// fallback, which is exactly what a dead host in a static NOCC_SERVERS list costs.

type cachedEntry struct {
	savedAt time.Time
	hosts   []ServerInfo
}

// DefaultCachePath is per-user, since /tmp is shared and the file is written on every browse.
func DefaultCachePath() string {
	return fmt.Sprintf("%s/nocc-discovered-servers-%d.txt", os.TempDir(), os.Getuid())
}

// BrowseCached returns cached discovery results if they are younger than ttl,
// otherwise it browses the network and refreshes the cache.
// A ttl of 0 disables caching entirely. Cache read/write errors are never fatal:
// on any problem we just do a fresh browse.
func BrowseCached(timeout time.Duration, ttl time.Duration, cachePath string) ([]ServerInfo, error) {
	if ttl <= 0 || cachePath == "" {
		return Browse(timeout)
	}

	if cached, err := readCache(cachePath); err == nil && time.Since(cached.savedAt) < ttl {
		return cached.hosts, nil
	}

	found, err := Browse(timeout)
	if err != nil {
		return nil, err
	}
	_ = writeCache(cachePath, found)
	return found, nil
}

// InvalidateCache drops the cached result, forcing the next browse to hit the network.
func InvalidateCache(cachePath string) {
	if cachePath != "" {
		_ = os.Remove(cachePath)
	}
}

// the cache format is deliberately a plain text file: one server per line,
// "host:port instance version cpus arch os", so it can be read (and deleted) by a human
func writeCache(cachePath string, found []ServerInfo) error {
	var sb strings.Builder
	sb.WriteString("# nocc mdns discovery cache, safe to delete\n")
	sb.WriteString(strconv.FormatInt(time.Now().Unix(), 10) + "\n")
	for _, info := range found {
		sb.WriteString(strings.Join([]string{
			info.HostPort,
			orDash(info.Instance),
			orDash(info.Version),
			strconv.Itoa(info.NumCPU),
			orDash(info.GOARCH),
			orDash(info.GOOS),
		}, " "))
		sb.WriteByte('\n')
	}

	// write via a temp file, so that a daemon reading the cache never sees a half-written list
	tmpPath := fmt.Sprintf("%s.%d.tmp", cachePath, os.Getpid())
	if err := os.WriteFile(tmpPath, []byte(sb.String()), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func readCache(cachePath string) (cachedEntry, error) {
	contents, err := os.ReadFile(cachePath)
	if err != nil {
		return cachedEntry{}, err
	}

	entry := cachedEntry{}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if entry.savedAt.IsZero() { // the first meaningful line is a unix timestamp
			savedAtUnix, err := strconv.ParseInt(line, 10, 64)
			if err != nil {
				return cachedEntry{}, fmt.Errorf("malformed timestamp %q", line)
			}
			entry.savedAt = time.Unix(savedAtUnix, 0)
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 6 {
			return cachedEntry{}, fmt.Errorf("malformed line %q", line)
		}
		numCPU, _ := strconv.Atoi(fields[3])
		entry.hosts = append(entry.hosts, ServerInfo{
			HostPort: fields[0],
			Instance: fromDash(fields[1]),
			Version:  fromDash(fields[2]),
			NumCPU:   numCPU,
			GOARCH:   fromDash(fields[4]),
			GOOS:     fromDash(fields[5]),
		})
	}
	if entry.savedAt.IsZero() {
		return cachedEntry{}, fmt.Errorf("no timestamp in %s", cachePath)
	}
	return entry, nil
}

// fields are space-separated, so an empty one is stored as "-" to keep the line parseable
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return strings.ReplaceAll(value, " ", "_")
}

func fromDash(value string) string {
	if value == "-" {
		return ""
	}
	return value
}

package server

import (
	"os"
	"strings"
	"sync"

	"github.com/VKCOM/nocc/internal/common"
)

type systemHeader struct {
	serverFileName string
	fileSize       int64
	fileSHA256     common.SHA256
}

// SystemHeadersCache stores info about system headers (typically, inside /usr/include).
// If a client wants to send /usr/include/math.h, and it's the same as here on the server,
// a client doesn't have to send its body,
// because we'll use a server's one instead of saving it to /tmp/nocc-server/client/{clientID}/usr/include/math.h.
// It's supposed, that system headers are in default include path of cxx on the server.
// Without system headers detection, everything still works, it's just a moment of optimization.
type SystemHeadersCache struct {
	mu      sync.RWMutex
	headers map[string]*systemHeader // from clientFileName to either nil (not a header) or systemHeader
}

func MakeSystemHeadersCache() (*SystemHeadersCache, error) {
	return &SystemHeadersCache{
		headers: make(map[string]*systemHeader, 512),
	}, nil
}

func (sHeaders *SystemHeadersCache) IsSystemHeader(headerPath string, fileSize int64, fileSHA256 common.SHA256) bool {
	if !strings.HasPrefix(headerPath, "/usr/") && !strings.HasPrefix(headerPath, "/Library/") {
		return false
	}

	sHeaders.mu.RLock()
	header, exists := sHeaders.headers[headerPath]
	sHeaders.mu.RUnlock()

	if exists {
		return header != nil && header.fileSize == fileSize && header.fileSHA256 == fileSHA256
	}

	stat, err := os.Stat(headerPath)
	if err != nil {
		sHeaders.mu.Lock()
		sHeaders.headers[headerPath] = nil // means that headerPath doesn't exist (=> not a system header)
		sHeaders.mu.Unlock()
		return false
	}

	headerSHA256, err := common.GetFileSHA256(headerPath)
	if err != nil {
		sHeaders.mu.Lock()
		sHeaders.headers[headerPath] = nil
		sHeaders.mu.Unlock()
		return false
	}

	header = &systemHeader{headerPath, stat.Size(), headerSHA256}
	sHeaders.mu.Lock()
	sHeaders.headers[headerPath] = header
	sHeaders.mu.Unlock()

	return header.fileSize == fileSize && header.fileSHA256 == fileSHA256
}

func (sHeaders *SystemHeadersCache) Count() int64 {
	sHeaders.mu.RLock()
	size := len(sHeaders.headers)
	sHeaders.mu.RUnlock()
	return int64(size)
}

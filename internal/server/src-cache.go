package server

import (
	"math/rand"
	"os"
	"strconv"
)

// SrcFileCache is a /tmp/nocc/cpp/src-cache directory, where uploaded .cpp/.h/etc. files are saved.
// It's supposed that sha256 uniquely identifies the file, that's why a map key doesn't contain size/mtime.
// It's useful to share files across clients (if one client has uploaded a file, the second takes it from cache).
// Also, it helps reuse files across the same client after it was considered inactive and deleted, but launched again.
type SrcFileCache struct {
	*FileCache
}

func MakeSrcFileCache(cacheDir string, limitBytes int64) (*SrcFileCache, error) {
	cache, err := MakeFileCache(cacheDir, limitBytes)
	if err != nil {
		return nil, err
	}

	return &SrcFileCache{cache}, nil
}

func (cache *SrcFileCache) MakeTempFileForUploadSaving(serverFileName string) (*os.File, error) {
	// path.Dir(serverFileName) is created in advance, see Client.MkdirAllForSession()
	fileNameTmp := serverFileName + "." + strconv.Itoa(rand.Int())
	return os.OpenFile(fileNameTmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, os.ModePerm)
}

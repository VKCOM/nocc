package client

import (
	"sync"

	"github.com/VKCOM/nocc/internal/common"
)

type includeCachedHFile struct {
	fileSize       int64         // size of file; -1 means that a file doesn't exist
	fileSHA256     common.SHA256 // hash of contents (but for pch it's a combined hash of dependencies)
	nestedIncludes []string      // [ /abs/path/to/sub/included_file.h, ... ] in order of appearance
}

// IncludesCache represents a structure that is kept in memory while the daemon is running.
// It helps reduce hard disk lookups for #include resolving.
type IncludesCache struct {
	// g++ / clang / etc. — detected on daemon start, on first `nocc` invocation
	cxxName string
	// default include dirs for current cxxName
	cxxDefIDirs IncludeDirs
	// how #include <math.h> is resolved to an /actual/path/to/math.h
	includesResolve map[string]string
	// properties of /actual/path/to/math.h (file/sha256 and nested #include list)
	hFilesInfo map[string]*includeCachedHFile

	mu sync.RWMutex
}

func MakeIncludesCache(cxxName string) (*IncludesCache, error) {
	cxxDefIDirs, err := GetDefaultCxxIncludeDirsOnLocal(cxxName)

	return &IncludesCache{
		cxxName:         cxxName,
		cxxDefIDirs:     cxxDefIDirs,
		includesResolve: make(map[string]string),
		hFilesInfo:      make(map[string]*includeCachedHFile),
	}, err
}

func (incCache *IncludesCache) GetIncludeResolve(quotedArg string) (hFileName string, exists bool) {
	if quotedArg[0] == '/' {
		hFileName, exists = quotedArg, true
		return
	}
	incCache.mu.RLock()
	hFileName, exists = incCache.includesResolve[quotedArg]
	incCache.mu.RUnlock()
	return
}

func (incCache *IncludesCache) AddIncludeResolve(quotedArg string, hFileName string) {
	incCache.mu.Lock()
	incCache.includesResolve[quotedArg] = hFileName
	incCache.mu.Unlock()
}

func (incCache *IncludesCache) GetHFileInfo(hFileName string) (hFileCached *includeCachedHFile, exists bool) {
	incCache.mu.RLock()
	hFileCached, exists = incCache.hFilesInfo[hFileName]
	incCache.mu.RUnlock()
	return
}

func (incCache *IncludesCache) AddHFileInfo(hFileName string, fileSize int64, fileSHA256 common.SHA256, nestedIncludes []string) {
	incCache.mu.Lock()
	incCache.hFilesInfo[hFileName] = &includeCachedHFile{fileSize, fileSHA256, nestedIncludes}
	incCache.mu.Unlock()
}

func (incCache *IncludesCache) Count() int {
	incCache.mu.RLock()
	count := len(incCache.hFilesInfo)
	incCache.mu.RUnlock()
	return count
}

func (incCache *IncludesCache) Clear() {
	incCache.mu.Lock()
	incCache.includesResolve = make(map[string]string)
	incCache.hFilesInfo = make(map[string]*includeCachedHFile)
	incCache.mu.Unlock()
}

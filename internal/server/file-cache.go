package server

import (
	"fmt"
	"os"
	"path"
	"sync"
	"sync/atomic"

	"github.com/VKCOM/nocc/internal/common"
)

type cachedFile struct {
	pathInCache string // /tmp/full/path/to/file.ext
	fileSize    int64
	lruNode     *lruNode
}

type lruNode struct {
	next, prev *lruNode
	key        common.SHA256
}

// FileCache is a base for ObjFileCache and SrcFileCache, see comments for them.
// It's a directory stored somewhere in /tmp where files could be saved and retrieved back by sha256.
// It's limited in size by lru (when its size exceeds a limit, the oldest accessed file is deleted).
// "Restoring from cache" is just a hard link to a new path.
type FileCache struct {
	table            map[common.SHA256]cachedFile
	lruTail, lruHead *lruNode
	mu               sync.RWMutex

	lastIndex   int64 // nb! atomic
	purgedCount int64 // nb! atomic
	cacheDir    string

	totalSizeOnDisk int64 // nb! atomic
	hardLimit       int64
	softLimit       int64
}

const shardsDirCount = 256

func createSubdirsForFileCache(cacheDir string) error {
	if err := os.MkdirAll(cacheDir, os.ModePerm); err != nil {
		return err
	}
	for i := 0; i < shardsDirCount; i++ {
		dir := path.Join(cacheDir, fmt.Sprintf("%X", i))
		if err := os.Mkdir(dir, os.ModePerm); err != nil {
			return err
		}
	}
	return nil
}

func MakeFileCache(cacheDir string, limitBytes int64) (*FileCache, error) {
	if err := createSubdirsForFileCache(cacheDir); err != nil {
		return nil, err
	}

	return &FileCache{
		table:     make(map[common.SHA256]cachedFile, 128*1024),
		cacheDir:  cacheDir,
		hardLimit: limitBytes,
		softLimit: int64(80.0 * (float64(limitBytes) / 100.0)),
	}, nil
}

func (cache *FileCache) ExistsInCache(key common.SHA256) bool {
	cache.mu.RLock()
	_, exists := cache.table[key]
	cache.mu.RUnlock()
	return exists
}

func (cache *FileCache) CreateHardLinkFromCache(destPath string, key common.SHA256) bool {
	cache.mu.Lock()
	cachedFile := cache.table[key]
	if cachedFile.lruNode != nil && cachedFile.lruNode != cache.lruHead {
		// cachedFile.lruNode != cache.lruHead => cachedFile.lruNode.prev != nil
		cachedFile.lruNode.prev.next = cachedFile.lruNode.next
		if cachedFile.lruNode.next == nil {
			// cachedFile.lruNode.next == nil => cachedFile.lruNode == cache.lruTail
			cache.lruTail = cachedFile.lruNode.prev
		} else {
			cachedFile.lruNode.next.prev = cachedFile.lruNode.prev
		}

		cachedFile.lruNode.prev = nil
		cachedFile.lruNode.next = cache.lruHead

		cache.lruHead.prev = cachedFile.lruNode
		cache.lruHead = cachedFile.lruNode
	}
	cache.mu.Unlock()

	if cachedFile.lruNode == nil {
		return false
	}

	err := os.MkdirAll(path.Dir(destPath), os.ModePerm)
	if err != nil {
		return false
	}
	err = os.Link(cachedFile.pathInCache, destPath)
	return err == nil || os.IsExist(err)
}

func (cache *FileCache) SaveFileToCache(srcPath string, key common.SHA256, fileSize int64) error {
	uniqueID := atomic.AddInt64(&cache.lastIndex, 1)
	fileName := path.Base(srcPath)
	cachedFileName := fmt.Sprintf("%X/%s.%X", uniqueID%shardsDirCount, fileName, uniqueID)
	cachedFilePath := path.Join(cache.cacheDir, cachedFileName)

	if err := os.Link(srcPath, cachedFilePath); err != nil {
		return err
	}

	newHead := &lruNode{key: key}
	value := cachedFile{pathInCache: cachedFilePath, fileSize: fileSize, lruNode: newHead}
	cache.mu.Lock()
	_, exists := cache.table[key]
	if !exists {
		atomic.AddInt64(&cache.totalSizeOnDisk, fileSize)
		cache.table[key] = value
		newHead.next = cache.lruHead
		if cache.lruHead != nil {
			cache.lruHead.prev = newHead
		}
		cache.lruHead = newHead
		if cache.lruTail == nil {
			cache.lruTail = newHead
		}
	}
	cache.mu.Unlock()

	if exists {
		_ = os.Remove(cachedFilePath)
	}

	cache.purgeLastElementsTillLimit(cache.hardLimit)
	return nil
}

func (cache *FileCache) PurgeLastElementsIfRequired() {
	cache.purgeLastElementsTillLimit(cache.softLimit)
}

func (cache *FileCache) GetFilesCount() int64 {
	cache.mu.Lock()
	elements := len(cache.table)
	cache.mu.Unlock()
	return int64(elements)
}

func (cache *FileCache) GetBytesOnDisk() int64 {
	return atomic.LoadInt64(&cache.totalSizeOnDisk)
}

func (cache *FileCache) GetPurgedFilesCount() int64 {
	return atomic.LoadInt64(&cache.purgedCount)
}

func (cache *FileCache) DropAll() {
	cache.mu.Lock()
	atomic.AddInt64(&cache.purgedCount, int64(len(cache.table)))
	atomic.StoreInt64(&cache.totalSizeOnDisk, 0)

	cache.table = make(map[common.SHA256]cachedFile, 128*1024)
	cache.lruHead = nil
	cache.lruHead = nil
	_ = os.RemoveAll(cache.cacheDir)
	_ = createSubdirsForFileCache(cache.cacheDir)

	cache.mu.Unlock()
}

func (cache *FileCache) purgeLastElementsTillLimit(cacheLimit int64) {
	for atomic.LoadInt64(&cache.totalSizeOnDisk) > cacheLimit {
		var removingFile cachedFile
		cache.mu.Lock()
		if tail := cache.lruTail; tail != nil {
			cache.lruTail = tail.prev
			cache.lruTail.next = nil
			removingFile = cache.table[tail.key]
			delete(cache.table, tail.key)
		}
		cache.mu.Unlock()

		if removingFile.lruNode != nil {
			_ = os.Remove(removingFile.pathInCache)
			atomic.AddInt64(&cache.totalSizeOnDisk, -removingFile.fileSize)
			atomic.AddInt64(&cache.purgedCount, 1)
		}
	}
}

package client

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/VKCOM/nocc/internal/common"
)

// ownIncludedArg describes an argument for an #include directive
type ownIncludedArg struct {
	insideStr     string // inside quotes
	isQuote       bool   // #include "arg" or #include <arg> (!isQuote == isAngle)
	isIncludeNext bool   // true for #include_next, not #include
}

// ownIncludesParser (this module) does the same work as `cxx -M` but much faster.
// It has methods that parse cpp/h files, find #include, resolve them and keep going recursively.
// It takes all -I / -iquote / -isystem dirs from cmd line into account.
// As a result, we have all dependencies, just like the C++ preprocessor was invoked.
//
// Unlike `cxx -M`, this is not a preprocessor, so it does nothing about #ifdef etc.
// Hence, it can find more includes than `cxx -M`, some of them may not exist, especially in system headers.
// This is not an error, because in practice, they are likely to be surrounded with #ifdef and never reached.
// But if own includes parsed finds fewer dependencies than `cxx -M`, it's a bug.
//
// Own includes can work only if paths are statically resolved: it can do nothing about #include MACRO().
// For instance, it can't analyze boost, as it's full of macro-includes.
// Only disabling own includes (invoking a real preprocessor) can help in that case.
type ownIncludesParser struct {
	includeDirs   IncludeDirs // -I and others for current invocation
	includesCache *IncludesCache

	err             error
	preallocatedBuf []byte // to read small files (one buffer is ok: includes are processed consecutively)

	// having #include "arg", it's searched it current folder, all -iquote, all -I, etc.
	// for every search attempt (for every full path), we store whether that path was already checked:
	// * a key in uniqSeen is a full path
	// * a value is either nil (a file doesn't exist) or a pointer (a file exists and is a dependency)
	uniqSeen map[string]*IncludedFile
	hFiles   []*IncludedFile // dependent includes, in order of appearance (= keys of non-nil uniqSeen)
}

func strChr(buffer []byte, chr byte, bufferSize int, offset int) int {
	idx := bytes.IndexByte(buffer[offset:bufferSize], chr)
	if idx == -1 {
		return -1
	}
	return idx + offset
}

func (included ownIncludedArg) String() string {
	hashInclude := "#include"
	if included.isIncludeNext {
		hashInclude = "#include_next"
	}
	if included.isQuote {
		return fmt.Sprintf("%s \"%s\"", hashInclude, included.insideStr)
	}
	return fmt.Sprintf("%s <%s>", hashInclude, included.insideStr)
}

// shouldCacheHFile detects for <foo.h> resolved as /usr/includes/somewhere/foo.h
// whether we should keep its size/sha256 in memory for future invocations in IncludesCache.
func (inc *ownIncludesParser) shouldCacheHFile(hFileName string) bool {
	// 1) cache angle includes: <foo.h>, since they would probably be included again
	// BUT! we cache only files in /usr/include and other "-isystem" dirs:
	// these locations don't depend on command-line invocation, they are cxx built-ins for local machine
	// we do NOT cache if <foo.h> is placed in "-I" directories, as -I can change between invocations
	// (for example, <php.h> for "-I /usr/include/php/20190902" and for "-I /usr/include/php/20170718" are different)
	// (various -I dirs are common for cmake usage)
	// the first loop is needed, as "-I" can be subdirs of "-isystem"
	for _, dir := range inc.includeDirs.dirsI {
		if strings.HasPrefix(hFileName, dir) {
			return false
		}
	}
	for _, dir := range inc.includeDirs.dirsIsystem {
		if strings.HasPrefix(hFileName, dir) {
			return true
		}
	}

	// 2) KPHP optimization: cache all instance classes, since they would probably be included again
	// these files remain in the same place, their locations on a hard disk don't depend on -I/-iquote
	if strings.Contains(hFileName, "/kphp/cl/") || strings.Contains(hFileName, "/kphp/cl_l/") {
		return true
	}

	// we can not safely cache all h files, as -iquote and others can affect their lookup in further invocations
	// (it means, that nested includes may map to different abs paths, so we'll have to look them up always)
	return false
}

// onHashInclude is a handler when we reached #include "arg"
// it finds what full path "arg" actually points to and processes that file recursively
func (inc *ownIncludesParser) onHashInclude(currentFileName string, includedArg *ownIncludedArg, tryPchInstead bool) *IncludedFile {
	var hFile *IncludedFile = nil

	inc.resolveIncludedArg(currentFileName, includedArg, func(hFileName string) bool {
		var seen bool

		hFile, seen = inc.uniqSeen[hFileName]
		if seen {
			return hFile != nil // we know, that either it doesn't exist or already processed
		}

		if tryPchInstead {
			if pchFile := LocateOwnPchFile(hFileName, inc.includesCache); pchFile != nil {
				inc.uniqSeen[hFileName] = pchFile // use pch even instead of original .h (original mustn't be uploaded)
				inc.uniqSeen[pchFile.fileName] = pchFile
				inc.hFiles = append(inc.hFiles, pchFile)
				return true
			}
		}

		// if hFileName is first seen, check file existence and start processing
		var cachedItem *includeCachedHFile
		var fileExists bool
		var fileSize int64
		var fileSHA256 common.SHA256
		var file *os.File
		var err error

		shouldCache := inc.shouldCacheHFile(hFileName)
		if shouldCache {
			cached, exists := inc.includesCache.GetHFileInfo(hFileName)
			if exists {
				cachedItem = cached
				fileExists = true
				fileSize = cachedItem.fileSize
				fileSHA256 = cachedItem.fileSHA256
			}
		}
		if cachedItem == nil {
			file, err = os.Open(hFileName)
			if err != nil && !os.IsNotExist(err) {
				logClient.Error("error opening", hFileName, err)
			}
			fileExists = err == nil
			if fileExists {
				stat, _ := file.Stat()
				fileSize = stat.Size()
				// fileSHA256 will be calculated later, after file read, and assigned to hFile via pointer
			}
		}

		if !fileExists {
			inc.uniqSeen[hFileName] = nil
			return false
		}

		hFile = &IncludedFile{hFileName, fileSize, fileSHA256}
		inc.uniqSeen[hFileName] = hFile
		inc.hFiles = append(inc.hFiles, hFile)

		if cachedItem != nil {
			_ = file.Close()
			for _, nestedInclude := range cachedItem.nestedIncludes { // nestedInclude is resolved, it starts from /
				inc.onHashInclude(hFileName, &ownIncludedArg{insideStr: nestedInclude}, false)
			}
			return true
		}

		inc.processHFile(hFile, file, shouldCache)
		return true
	})

	return hFile
}

// resolveIncludedArg enumerates all possible paths for #include "arg"
// depending on "-I" options, whether it's "arg" or <arg> and so on.
// For each theoretically available full path, it invokes onEachResolveAttempt that returns whether a file exists.
func (inc *ownIncludesParser) resolveIncludedArg(currentFileName string, includedArg *ownIncludedArg, onEachResolveAttempt func(hFileName string) bool) {
	eachFn := onEachResolveAttempt

	if includedArg.isIncludeNext {
		// example: currentFileName = "/usr/include/c++/8/cstdlib", includedArg = #include_next <stdlib.h>
		// includeDirs = [ 0 "/home", 1 "/usr/include/c++/8", 2 "/usr/include/c++/8/backward", 3 "/usr/include" ]
		// we try to locate stdlib.h in 2 and 3 (onEachResolveAttempt will be called for 2/stdlib.h and 3/stdlib.h)
		curIncludeDirSeen := false
		eachFn = func(hFileName string) bool {
			if !curIncludeDirSeen {
				includeDir := hFileName[0 : len(hFileName)-len(includedArg.insideStr)]
				if strings.HasPrefix(currentFileName, includeDir) {
					curIncludeDirSeen = true
				}
				return false
			}
			return onEachResolveAttempt(hFileName)
		}
	}

	isAngle := !includedArg.isQuote && !includedArg.isIncludeNext
	if isAngle {
		hFileName, exists := inc.includesCache.GetIncludeResolve(includedArg.insideStr)
		if exists {
			if hFileName != "NO" {
				onEachResolveAttempt(hFileName)
			}
			return
		}
		eachFn = func(hFileName string) bool {
			fileExists := onEachResolveAttempt(hFileName)
			if fileExists && inc.shouldCacheHFile(hFileName) {
				inc.includesCache.AddIncludeResolve(includedArg.insideStr, hFileName)
			}
			return fileExists
		}
	}

	if includedArg.insideStr[0] == '/' { // #include "/abs/path" — the only option, don't traverse dirs
		eachFn(includedArg.insideStr)
		return
	}
	if includedArg.isQuote {
		if eachFn(path.Join(path.Dir(currentFileName), includedArg.insideStr)) {
			return
		}
		for _, dir := range inc.includeDirs.dirsIquote {
			if eachFn(path.Join(dir, includedArg.insideStr)) {
				return
			}
		}
	}
	for _, dir := range inc.includeDirs.dirsI {
		if eachFn(path.Join(dir, includedArg.insideStr)) {
			return
		}
	}
	for _, dir := range inc.includeDirs.dirsIsystem {
		if eachFn(path.Join(dir, includedArg.insideStr)) {
			return
		}
	}

	if isAngle {
		// even for not found, store that fact in cache, so that nocc won't try to find them on the next invocation
		inc.includesCache.AddIncludeResolve(includedArg.insideStr, "NO")
	}
}

// collectIncludeStatementsInFile finds all #include "arg" in a file, in order of appearance
// C and C++ style comments are respected, includes aren't found within them
func (inc *ownIncludesParser) collectIncludeStatementsInFile(buffer []byte) (includes []*ownIncludedArg) {
	const (
		stateNone = iota
		stateAfterHash
		stateAfterInclude
		stateInsideQuoteBrackets
		stateInsideAngleBrackets
	)
	state := stateNone
	isInsideIncludeNext := false

	bufferSize := len(buffer)
	offset := 0
	lastHash := bytes.LastIndexByte(buffer, '#')
	if lastHash != -1 {
		if string(buffer[lastHash:lastHash+6]) == "#endif" {
			lastHash = bytes.LastIndexByte(buffer[:lastHash-1], '#')
		}
		if lastHash != -1 {
			newLineIdx := strChr(buffer, '\n', bufferSize, lastHash)
			if newLineIdx != -1 {
				bufferSize = newLineIdx + 1
			}
		}
	}

	nextHash := strChr(buffer, '#', bufferSize, 0)
	nextSlash := strChr(buffer, '/', bufferSize, 0)
	start := 0
Loop:
	for offset < bufferSize {
		switch state {
		case stateNone:
			if nextHash != -1 && nextHash < offset {
				nextHash = strChr(buffer, '#', bufferSize, offset)
			}
			if nextHash == -1 {
				break Loop
			}
			if nextSlash != -1 && nextSlash < offset {
				nextSlash = strChr(buffer, '/', bufferSize, offset)
			}
			if nextSlash != -1 && nextSlash < nextHash {
				offset = nextSlash
				if buffer[offset+1] == '/' {
					offset = strChr(buffer, '\n', bufferSize, offset)
				} else if buffer[offset+1] == '*' {
					for ok := true; ok; ok = buffer[offset-1] != '*' { // do while
						offset = strChr(buffer, '/', bufferSize, offset+1)
						if offset == -1 {
							break Loop
						}
					}
				}
			} else {
				offset = nextHash
				state = stateAfterHash
			}

		case stateAfterHash:
			switch buffer[offset] {
			case ' ':
			case '\t':
				break
			default:
				if bufferSize > offset+12 && string(buffer[offset:offset+12]) == "include_next" {
					state = stateAfterInclude
					offset += 11
					isInsideIncludeNext = true
				} else if bufferSize > offset+7 && string(buffer[offset:offset+7]) == "include" {
					state = stateAfterInclude
					offset += 6
					isInsideIncludeNext = false
				} else {
					state = stateNone
				}
			}

		case stateAfterInclude:
			switch buffer[offset] {
			case ' ':
			case '\t':
				break
			case '<':
				start = offset + 1
				state = stateInsideAngleBrackets
			case '"':
				start = offset + 1
				state = stateInsideQuoteBrackets
			default:
				state = stateNone // buggy code
			}

		case stateInsideAngleBrackets:
			switch buffer[offset] {
			case '\n':
				state = stateNone // buggy code
			case '>':
				includes = append(includes, &ownIncludedArg{string(buffer[start:offset]), false, isInsideIncludeNext})
				state = stateNone
			}

		case stateInsideQuoteBrackets:
			switch buffer[offset] {
			case '\n':
				state = stateNone // buggy code
			case '"':
				includes = append(includes, &ownIncludedArg{string(buffer[start:offset]), true, isInsideIncludeNext})
				state = stateNone
			}
		}

		offset++
	}

	return
}

func (inc *ownIncludesParser) processHFile(hFile *IncludedFile, file *os.File, shouldCache bool) {
	fileSHA256, buffer, err := CalcSHA256OfFile(file, hFile.fileSize, inc.preallocatedBuf)
	_ = file.Close() // close a file before digging into nested .h, not to keep open descriptors
	if err != nil {
		inc.err = err // keep the last error here
		return
	}

	hFile.fileSHA256 = fileSHA256
	includeStatements := inc.collectIncludeStatementsInFile(buffer)

	if !shouldCache {
		for _, includedArg := range includeStatements {
			inc.onHashInclude(hFile.fileName, includedArg, false)
		}
	} else {
		nestedIncludes := make([]string, 0, len(includeStatements))
		for _, includedArg := range includeStatements {
			if hNested := inc.onHashInclude(hFile.fileName, includedArg, false); hNested != nil {
				nestedIncludes = append(nestedIncludes, hNested.fileName)
			}
		}
		inc.includesCache.AddHFileInfo(hFile.fileName, hFile.fileSize, hFile.fileSHA256, nestedIncludes)
	}
}

func (inc *ownIncludesParser) processCppInFile(cppInFile string, searchForPch bool, explicitIncludes []string) (IncludedFile, error) {
	// on some systems, g++ includes <stdc-predef.h> implicitly
	stdcPredefH := ownIncludedArg{"stdc-predef.h", false, false}
	inc.onHashInclude(cppInFile, &stdcPredefH, false)

	// also, loop through "-include {file}" mentioned in cmd line, treating them like #include <file>
	// clang uses "-include {hFile}" to specify looking up for a precompiled header, act the same
	for _, iFile := range explicitIncludes {
		exInclude := ownIncludedArg{iFile, false, false}
		inc.onHashInclude(cppInFile, &exInclude, searchForPch)
	}

	// now, loop through #include in cppInFile, analyzing them recursively
	fileSHA256, buffer, err := CalcSHA256OfFileName(cppInFile, inc.preallocatedBuf)
	if err != nil {
		return IncludedFile{}, err
	}
	cppFile := IncludedFile{cppInFile, int64(len(buffer)), fileSHA256}
	includes := inc.collectIncludeStatementsInFile(buffer)

	for idx, includedArg := range includes {
		// according to .gch search rules, an #include can be replaced with a precompiled header
		// only if it's the first token in a file, only if included directly from .cpp (not from nested includes)
		// we don't have "tokens", we just try .gch for the first include
		tryPchInstead := idx == 0 && searchForPch
		inc.onHashInclude(cppInFile, includedArg, tryPchInstead)
	}
	return cppFile, inc.err
}

// CollectDependentIncludesByOwnParser executes the own includes parser.
// It should return the same results (or a bit more) as "cxx -M".
func CollectDependentIncludesByOwnParser(includesCache *IncludesCache, cppInFile string, includeDirs IncludeDirs) (hFiles []*IncludedFile, cppFile IncludedFile, err error) {
	inc := ownIncludesParser{
		includeDirs:     includeDirs,
		includesCache:   includesCache,
		preallocatedBuf: make([]byte, 32*1024), // most .h files are less than 32k, they'll use the same buffer
		uniqSeen:        make(map[string]*IncludedFile, 20),
		hFiles:          make([]*IncludedFile, 0, 8),
	}

	// we'll try to search for precompiled headers regardless of -fpch-preprocess and -include options
	searchForPch := isSourceFileName(cppInFile)
	cppFile, err = inc.processCppInFile(cppInFile, searchForPch, inc.includeDirs.filesI)
	hFiles = inc.hFiles

	// sorting is not needed, since there is no parallelization while collecting includes for a cpp file
	// it means, that the result is stable from time to time: includes are listed in order of appearance
	return
}

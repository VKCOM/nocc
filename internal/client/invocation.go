package client

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	invokedUnsupported = iota
	invokedForCompilingCpp
	invokedForCompilingPch
	invokedForLinking
)

// Invocation describes one `nocc` invocation inside a daemon.
// When `nocc g++ ...` is called, it pipes cmdLine to a background Daemon, which serves them in parallel.
// If this invocation is to compile .cpp to .o, it maps bidirectionally to server.Session.
type Invocation struct {
	invokeType int   // one of the constants above
	err        error // any error occurred while parsing/uploading/compiling/receiving

	createTime time.Time // used for local timeout
	sessionID  uint32    // incremental while a daemon is alive

	// cmdLine is parsed to the following fields:
	cppInFile  string      // absolute path to the input file (.cpp for compilation, .h for pch generation)
	objOutFile string      // absolute path to the output file (.o for compilation, .gch/.pch for pch generation)
	cxxName    string      // g++ / clang / etc.
	cxxArgs    []string    // args like -Wall, -fpch-preprocess and many more, except:
	cxxIDirs   IncludeDirs // -I / -iquote / -isystem go here
	depsFlags  DepCmdFlags // -MD -MF file and others, used for .d files generation (not passed to server)

	waitUploads int32 // files still waiting for upload to finish; 0 releases wgUpload; see Invocation.DoneUploadFile
	doneRecv    int32 // 1 if o file received or failed receiving; 1 releases wgRecv; see Invocation.DoneRecvObj
	wgUpload    sync.WaitGroup
	wgRecv      sync.WaitGroup

	// when remote compilation starts, the server starts a server.Session (with the same sessionID)
	// after it finishes, we have these fields filled (and objOutFile saved)
	cxxExitCode int
	cxxStdout   []byte
	cxxStderr   []byte
	cxxDuration int32

	summary       *InvocationSummary
	includesCache *IncludesCache // = Daemon.includesCache[cxxName]
}

func isSourceFileName(fileName string) bool {
	return strings.HasSuffix(fileName, ".cpp") ||
		strings.HasSuffix(fileName, ".cc") ||
		strings.HasSuffix(fileName, ".cxx") ||
		strings.HasSuffix(fileName, ".c")
}

func isHeaderFileName(fileName string) bool {
	return strings.HasSuffix(fileName, ".h") ||
		strings.HasSuffix(fileName, ".hh") ||
		strings.HasSuffix(fileName, ".hxx") ||
		strings.HasSuffix(fileName, ".hpp")
}

func pathAbs(cwd string, relPath string) string {
	if relPath[0] == '/' {
		return relPath
	}
	return filepath.Join(cwd, relPath)
}

func ParseCmdLineInvocation(daemon *Daemon, cwd string, cmdLine []string) (invocation *Invocation) {
	invocation = &Invocation{
		createTime:    time.Now(),
		sessionID:     atomic.AddUint32(&daemon.totalInvocations, 1),
		cxxName:       cmdLine[0],
		cxxArgs:       make([]string, 0, 10),
		cxxIDirs:      MakeIncludeDirs(),
		summary:       MakeInvocationSummary(),
		includesCache: daemon.GetOrCreateIncludesCache(cmdLine[0]),
	}

	parseArgFile := func(key string, arg string, argIndex *int) (string, bool) {
		if arg == key { // -I /path
			if *argIndex+1 < len(cmdLine) {
				*argIndex++
				if cmdLine[*argIndex] == "-Xclang" { // -Xclang -include -Xclang {file}
					*argIndex++
				}
				return pathAbs(cwd, cmdLine[*argIndex]), true
			} else {
				invocation.err = fmt.Errorf("unsupported command-line: no argument after %s", arg)
				return "", false
			}
		} else if strings.HasPrefix(arg, key) { // -I/path
			return pathAbs(cwd, arg[len(key):]), true
		}
		return "", false
	}

	parseArgStr := func(key string, arg string, argIndex *int) string {
		if arg == key {
			if *argIndex+1 < len(cmdLine) {
				*argIndex++
				return cmdLine[*argIndex]
			} else {
				invocation.err = fmt.Errorf("unsupported command-line: no argument after %s", arg)
				return ""
			}
		}
		return ""
	}

	for i := 1; i < len(cmdLine); i++ {
		arg := cmdLine[i]
		if len(arg) == 0 {
			continue
		}
		if arg[0] == '-' {
			if oFile, ok := parseArgFile("-o", arg, &i); ok {
				invocation.objOutFile = oFile
				invocation.depsFlags.SetCmdOutputFile(strings.TrimPrefix(arg, "-o"))
				continue
			} else if dir, ok := parseArgFile("-I", arg, &i); ok {
				invocation.cxxIDirs.dirsI = append(invocation.cxxIDirs.dirsI, dir)
				continue
			} else if dir, ok := parseArgFile("-iquote", arg, &i); ok {
				invocation.cxxIDirs.dirsIquote = append(invocation.cxxIDirs.dirsIquote, dir)
				continue
			} else if dir, ok := parseArgFile("-isystem", arg, &i); ok {
				invocation.cxxIDirs.dirsIsystem = append(invocation.cxxIDirs.dirsIsystem, dir)
				continue
			} else if iFile, ok := parseArgFile("-include", arg, &i); ok {
				invocation.cxxIDirs.filesI = append(invocation.cxxIDirs.filesI, iFile)
				continue
			} else if arg == "-march=native" {
				invocation.err = fmt.Errorf("-march=native can't be launched remotely")
				return
			} else if arg == "-I-" || arg == "-E" || arg == "-nostdinc" || arg == "-nostdinc++" ||
				strings.HasPrefix(arg, "-iprefix") || strings.HasPrefix(arg, "-idirafter") || strings.HasPrefix(arg, "--sysroot") {
				invocation.err = fmt.Errorf("unsupported option: %s", arg)
				return
			} else if arg == "-isysroot" {
				// an exception for local development when "remote" is also local, but generally unsupported yet
				if len(daemon.remoteConnections) == 1 && daemon.remoteConnections[0].remoteHostPort == "127.0.0.1:43210" {
					invocation.cxxArgs = append(invocation.cxxArgs, arg, cmdLine[i+1])
					i++
					continue
				}
				invocation.err = fmt.Errorf("unsupported option: %s", arg)
				return
			} else if arg == "-Xarch_arm64" {
				// todo if it's placed before -include, it should remain before it after cmd line reconstruction; for now, skip
				continue
			} else if mfFile := parseArgStr("-MF", arg, &i); mfFile != "" {
				invocation.depsFlags.SetCmdFlagMF(pathAbs(cwd, mfFile))
				continue
			} else if strArg := parseArgStr("-MT", arg, &i); strArg != "" {
				invocation.depsFlags.SetCmdFlagMT(strArg)
				continue
			} else if strArg := parseArgStr("-MQ", arg, &i); strArg != "" {
				invocation.depsFlags.SetCmdFlagMQ(strArg)
				continue
			} else if arg == "-MD" {
				invocation.depsFlags.SetCmdFlagMD()
				continue
			} else if arg == "-MMD" {
				invocation.depsFlags.SetCmdFlagMMD()
				continue
			} else if arg == "-MP" {
				invocation.depsFlags.SetCmdFlagMP()
				continue
			} else if arg == "-M" || arg == "-MM" || arg == "-MG" {
				// these dep flags are unsupported yet, cmake doesn't use them
				invocation.err = fmt.Errorf("unsupported option: %s", arg)
				return
			} else if arg == "-Xclang" && i < len(cmdLine)-1 { // "-Xclang {xArg}" — leave as is, unless we need to parse arg
				xArg := cmdLine[i+1]
				if xArg == "-I" || xArg == "-iquote" || xArg == "-isystem" || xArg == "-include" {
					continue // like "-Xclang" doesn't exist
				}
				invocation.cxxArgs = append(invocation.cxxArgs, "-Xclang", xArg)
				i++
				continue
			}
		} else if isSourceFileName(arg) || isHeaderFileName(arg) {
			if invocation.cppInFile != "" {
				invocation.err = fmt.Errorf("unsupported command-line: multiple input source files")
				return
			}
			invocation.cppInFile = pathAbs(cwd, arg)
			invocation.depsFlags.SetCmdInputFile(arg)
			continue
		} else if strings.HasSuffix(arg, ".o") || strings.HasPrefix(arg, ".so") || strings.HasSuffix(arg, ".a") {
			invocation.invokeType = invokedForLinking
			return
		}
		invocation.cxxArgs = append(invocation.cxxArgs, arg)
	}

	if invocation.err != nil {
		return
	}

	if invocation.cppInFile == "" {
		invocation.err = fmt.Errorf("unsupported command-line: no input file specified")
	} else if strings.HasSuffix(invocation.objOutFile, ".o") {
		invocation.invokeType = invokedForCompilingCpp
	} else if strings.Contains(invocation.objOutFile, ".gch") || strings.Contains(invocation.objOutFile, ".pch") {
		invocation.invokeType = invokedForCompilingPch
	} else {
		invocation.err = fmt.Errorf("unsupported output file extension: %s", invocation.objOutFile)
	}
	return
}

// CollectDependentIncludes finds dependencies for an input .cpp file.
// "dependencies" are typically all reachable .h files at any level, and probably precompiled headers.
// There are two modes of finding dependencies:
// 1. Natively: invoke "cxx -M" (it invokes preprocessor only).
// 2. Own includes parser, which works much faster and theoretically should return the same (or a bit more) results.
func (invocation *Invocation) CollectDependentIncludes(disableOwnIncludes bool) (hFiles []*IncludedFile, cppFile IncludedFile, err error) {
	if disableOwnIncludes {
		return CollectDependentIncludesByCxxM(invocation.includesCache, invocation.cxxName, invocation.cppInFile, invocation.cxxArgs, invocation.cxxIDirs)
	}

	includeDirs := invocation.cxxIDirs
	includeDirs.MergeWith(invocation.includesCache.cxxDefIDirs)

	return CollectDependentIncludesByOwnParser(invocation.includesCache, invocation.cppInFile, includeDirs)
}

func (invocation *Invocation) DoneRecvObj(err error) {
	if atomic.SwapInt32(&invocation.doneRecv, 1) == 0 {
		if err != nil {
			invocation.err = err
		}
		invocation.wgRecv.Done()
	}
}

func (invocation *Invocation) DoneUploadFile(err error) {
	if err != nil {
		invocation.err = err
	}
	atomic.AddInt32(&invocation.waitUploads, -1)
	invocation.wgUpload.Done() // will end up after all required files uploaded/failed
}

func (invocation *Invocation) ForceInterrupt(err error) {
	logClient.Error("force interrupt", "sessionID", invocation.sessionID, invocation.cppInFile, err)
	// release invocation.wgUpload
	for atomic.LoadInt32(&invocation.waitUploads) != 0 {
		invocation.DoneUploadFile(err)
	}
	// release invocation.wgDone
	invocation.DoneRecvObj(err)
}

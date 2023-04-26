package server

import (
	"fmt"
	"os"
	"path"
	"sync"
	"time"

	"github.com/VKCOM/nocc/internal/common"
)

type compiledPchItem struct {
	ownPch      *common.OwnPch
	realHFile   string
	realPchFile string
}

// PchCompilation is a singleton inside NoccServer that stores compiled .nocc-pch files.
// Unlike src cache, here there is no lru (it's supposed that there won't be many pch files).
// Inside allPchDir, there are "basename-hash/" subdirs with extracted sources and compiled .gch/.pch.
type PchCompilation struct {
	allPchDir string

	compiledPchList map[common.SHA256]*compiledPchItem
	mu              sync.Mutex
}

func MakePchCompilation(allPchDir string) (*PchCompilation, error) {
	return &PchCompilation{
		allPchDir:       allPchDir,
		compiledPchList: make(map[common.SHA256]*compiledPchItem, 10),
	}, nil
}

func (pchCompilation *PchCompilation) PrepareServerCxxCmdLine(ownPch *common.OwnPch, rootDir string) []string {
	cxxCmdLine := make([]string, 0, len(ownPch.CxxIDirs)+len(ownPch.CxxArgs)+3)

	// loop through -I {dir} / -include {file} / etc., converting client {dir} to server path
	for i := 0; i < len(ownPch.CxxIDirs); i += 2 {
		arg := ownPch.CxxIDirs[i]
		serverIdir := rootDir + ownPch.CxxIDirs[i+1]
		cxxCmdLine = append(cxxCmdLine, arg, serverIdir)
	}
	// append -Wall and other cxx args
	cxxCmdLine = append(cxxCmdLine, ownPch.CxxArgs...)
	// append output (.gch/.pch) and input (a header generated from)
	return append(cxxCmdLine, "-o", rootDir+ownPch.OrigPchFile, rootDir+ownPch.OrigHFile)
}

// CompileOwnPchOnServer is called when a client uploads a .nocc-pch file.
// This file contains all dependencies, that are extracted to a separate folder, and a real .gch/.pch is produced.
func (pchCompilation *PchCompilation) CompileOwnPchOnServer(noccServer *NoccServer, ownPchFile string) error {
	ownPch, err := common.ParseOwnPchFile(ownPchFile)
	if err != nil {
		logServer.Error("failed to parse own pch file", ownPchFile, err)
		return err
	}

	rootDir := path.Join(pchCompilation.allPchDir, path.Base(ownPch.OrigHFile)+"-"+ownPch.PchHash.ToShortHexString())
	compiledPch := &compiledPchItem{
		ownPch:      ownPch,
		realHFile:   path.Join(rootDir, ownPch.OrigHFile),
		realPchFile: path.Join(rootDir, ownPch.OrigPchFile),
	}

	// if rootDir already exists — then another client already started (and maybe finished) compiling this pch
	// then, wait for a .gch/.pch become ready
	if _, err = os.Stat(rootDir); err == nil {
		logServer.Info(0, "another call is being compiling pch, wait", ownPch.PchHash.ToLongHexString())
		if pchCompilation.waitUntilCompiled(ownPch.PchHash) {
			return pchCompilation.CreateHardLinkFromRealPch(ownPchFile, ownPch.PchHash)
		}
		logServer.Error("failed to wait until another call compiles pch, try again", rootDir)
		_ = os.RemoveAll(rootDir)
	}

	err = ownPch.ExtractAllDepsToRootDir(rootDir)
	if err != nil {
		logServer.Error("failed to extract own pch file", ownPchFile, "to rootDir", rootDir, err)
		return err
	}

	logServer.Info(0, "compiling own pch file", ownPch.PchHash.ToLongHexString(), ownPch.OwnPchFile)
	cxxCmdLine := pchCompilation.PrepareServerCxxCmdLine(ownPch, rootDir)
	err = noccServer.CxxLauncher.launchServerCxxForPch(ownPch.CxxName, cxxCmdLine, rootDir, noccServer)
	if err != nil {
		return err
	}
	logServer.Info(0, "compiled own pch", compiledPch.realPchFile)

	pchCompilation.mu.Lock()
	pchCompilation.compiledPchList[ownPch.PchHash] = compiledPch
	pchCompilation.mu.Unlock()

	return pchCompilation.CreateHardLinkFromRealPch(ownPchFile, ownPch.PchHash)
}

// waitUntilCompiled is called when rootDir for pch compilation already exists.
// It means, that two equal pch files were uploaded by two clients, the first call created dir and started cxx,
// and the second call has just to wait until a resulting .gch/.pch becomes existing.
// Here we are the "second call" and just wait.
func (pchCompilation *PchCompilation) waitUntilCompiled(ownPchHash common.SHA256) bool {
	start := time.Now()
	for time.Since(start) < 10*time.Second {
		time.Sleep(20 * time.Millisecond)

		pchCompilation.mu.Lock()
		_, exists := pchCompilation.compiledPchList[ownPchHash]
		pchCompilation.mu.Unlock()
		if exists {
			return true
		}
	}
	return false
}

// CreateHardLinkFromRealPch makes `ln` to a desired folder.
// When a client tells that 1.cpp depends on /path/to/all-headers.nocc-pch, we recreate
// /path/to/all-headers.h and /path/to/all-headers.gch as hard links.
// This makes #include "all-headers.h" inside 1.cpp work as expected.
func (pchCompilation *PchCompilation) CreateHardLinkFromRealPch(ownPchName string, ownPchHash common.SHA256) error {
	pchCompilation.mu.Lock()
	compiledPch := pchCompilation.compiledPchList[ownPchHash]
	pchCompilation.mu.Unlock()

	if compiledPch == nil {
		return fmt.Errorf("can't find compiled pch by hash %s", ownPchHash.ToLongHexString())
	}

	clientHFile := path.Join(path.Dir(ownPchName), path.Base(compiledPch.ownPch.OrigHFile))
	clientPchFile := path.Join(path.Dir(ownPchName), path.Base(compiledPch.ownPch.OrigPchFile))

	_ = os.Link(compiledPch.realHFile, clientHFile)
	return os.Link(compiledPch.realPchFile, clientPchFile)
}

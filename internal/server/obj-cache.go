package server

import (
	"crypto/sha256"
	"fmt"
	"path"
	"strings"

	"github.com/VKCOM/nocc/internal/common"
)

// ObjFileCache is a /tmp/nocc/obj/obj-cache directory, where the resulting .o files are saved.
// Its purpose is to reuse a ready .o file if the same .cpp is requested to be compiled again.
// This is especially useful to share .o files across build agents:
// if one build agent compiles the master branch, other build agents can reuse ready .o for every .cpp.
// The hardest problem is how to detect that "this .cpp was already compiled, we can use .o".
// See ObjFileCache.MakeObjCacheKey.
type ObjFileCache struct {
	*FileCache

	// next to obj-cache, there is a /tmp/nocc/obj/cxx-out directory (session.objOutFile point here)
	// after being compiled, files from here are hard linked to obj-cache
	objTmpDir string
}

func MakeObjFileCache(cacheDir string, objTmpDir string, limitBytes int64) (*ObjFileCache, error) {
	cache, err := MakeFileCache(cacheDir, limitBytes)
	if err != nil {
		return nil, err
	}

	return &ObjFileCache{cache, strings.TrimSuffix(objTmpDir, "/")}, nil
}

// MakeObjCacheKey creates a unique key (sha256) for an input .cpp file and all its dependencies.
// If this exact .cpp file with these exact dependencies was already compiled (even by another client),
// we can reuse stored .o and respond immediately, without actual cxx invocation.
//
// Cxx compilation depends not only on files, but on other options too, the final cxxCmdLine looks like
// > g++ -Wall -fpch-preprocess ... -iquote /tmp/client1/headers -o /tmp/client1/some.cpp.123.o /tmp/client1/some.cpp
// We want to reuse a ready .o file if and only if:
// * the .cpp file is the same (its name and sha256)
// * all dependent .h/.nocc-pch/etc. are the same (their count, order, size, sha256)
// * all C++ compiler options are the same
//
// The problem is with the last point. cxxCmdLine contains -I and other options that vary between clients:
// > -iquote /tmp/nocc/cpp/clients/{clientID}/home/{username}/proj -I /tmp/gch/{random_hash} -o ...{random_int}.o
// These are different options, but in fact, they should be considered the same.
// That's why we don't take include paths into account when calculating a hash from cxxCmdLine.
// The assumption is: if all deps are equal, their actual paths/names don't matter.
func (cache *ObjFileCache) MakeObjCacheKey(cxxName string, cxxArgs []string, sessionFiles []*fileInClientDir, cppInFile string) common.SHA256 {
	hasher := sha256.New()

	hasher.Write([]byte(cxxName))
	for _, arg := range cxxArgs {
		hasher.Write([]byte(arg))
	}
	hasher.Write([]byte(path.Base(cppInFile))) // not a full path, as it varies between clients

	sha256xor := common.MakeSHA256Struct(hasher)
	sha256xor.B8_15 ^= uint64(len(cxxArgs))
	sha256xor.B16_23 ^= uint64(len(sessionFiles))
	for _, file := range sessionFiles {
		sha256xor.XorWith(&file.fileSHA256)
		sha256xor.B0_7 ^= uint64(file.fileSize)
	}

	return sha256xor
}

// GenerateObjOutFileName generates session.objOutFile (destination for C++ compiler launched on a server)
func (cache *ObjFileCache) GenerateObjOutFileName(session *Session) string {
	return fmt.Sprintf("%s/%s.%d.o", cache.objTmpDir, session.client.clientID, session.sessionID)
}

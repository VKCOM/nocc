package server

import (
	"crypto/sha256"
	"path"
	"strconv"
	"strings"

	"github.com/VKCOM/nocc/internal/common"
)

// ObjFileCache is a /tmp/nocc-server/obj-cache directory, where the resulting .o files are saved.
// Its purpose is to reuse a ready .o file if the same .cpp is requested to be compiled again.
// This is especially useful to share .o files across build agents:
// if one build agent compiles the master branch, other build agents can reuse ready .o for every .cpp.
// The hardest problem is how to detect that "this .cpp was already compiled, we can use .o".
// See ObjFileCache.MakeObjCacheKey.
type ObjFileCache struct {
	*FileCache
}

func MakeObjFileCache(cacheDir string, limitBytes int64) (*ObjFileCache, error) {
	cache, err := MakeFileCache(cacheDir, limitBytes)
	if err != nil {
		return nil, err
	}

	return &ObjFileCache{cache}, nil
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
// > -iquote /tmp/nocc-server/clients/{clientID}/home/{username}/proj -I /tmp/gch/{random_hash} -o ...{random_int}.o
// These are different options, but in fact, they should be considered the same.
// That's why we don't take include paths into account when calculating a hash from cxxCmdLine.
// The assumption is: if all deps are equal, their actual paths/names don't matter.
func (cache *ObjFileCache) MakeObjCacheKey(session *Session) common.SHA256 {
	depsStr := strings.Builder{}
	depsStr.Grow(4096)

	depsStr.WriteString(session.cxxName)
	depsStr.WriteString("; args = ")

	for i := 0; i < len(session.cxxCmdLine)-1; i++ { // -1, as the last arg is an input file
		arg := session.cxxCmdLine[i]
		if arg == "-I" || arg == "-iquote" || arg == "-isystem" || arg == "-include" || arg == "-o" {
			i++
		} else {
			depsStr.WriteString(arg)
			depsStr.WriteString(" ")
		}
	}

	depsStr.WriteString("; deps ")
	depsStr.WriteString(strconv.Itoa(len(session.files)))
	depsStr.WriteString("; in ")
	depsStr.WriteString(path.Base(session.cppInFile)) // just a protection; not a full path, as it varies between clients

	hasher := sha256.New()
	hasher.Write([]byte(depsStr.String()))

	sha256xor := common.MakeSHA256Struct(hasher)
	for _, file := range session.files {
		sha256xor.XorWith(&file.fileSHA256)
		sha256xor.B0_7 ^= uint64(file.fileSize)
	}

	return sha256xor
}

# nocc architecture

Here we describe some aspects of `nocc` internals.


<p><br></p>

## Daemon, client, and server

<p align="center">
    <img src="img/nocc-daemon.drawio.png" alt="daemon" height="356">
</p>

`nocc` itself is a C++ lightweight binary that pipes command-line to `nocc-daemon`.

When a build system (make / KPHP / etc.) simultaneously calls compilation jobs like
```bash
nocc g++ ... 1.cpp
nocc g++ ... 2.cpp
# other 100k jobs
```
then this binary is executed, actually. All its sources are present in [a single file](../cmd/nocc.cpp). 
`nocc` processes start and die, while a daemon is running.

`nocc-daemon` is a background process written in Go. 
It's started by the very first `nocc` invocation and dies in 15 seconds after the last `nocc` process dies (it's an assumption *"a build has finished"*).
The daemon keeps all connections with grpc streams and stores an includes cache in memory. 

When a new `nocc` process starts and pipes a command-line to the daemon, the daemon parses it. Parsing could result in:
* *(typical case)* invoked for compiling .cpp to .o
* invoked for compiling a precompiled header
* invoked for linking
* a command-line has unsupported options (`--sysroot` and some others are not handled yet)
* a command-line could not be parsed (`-o` does not exist, or an input file not detected, etc.)
* remote compilation is not available (e.g. `-march=native`)

Compiling a cpp file is called **an invocation** (see [invocation.go](../internal/client/invocation.go)). 
Every invocation has an autoincrement *sessionID* and is compiled remotely. 
Precompiled headers are handled in a special way (see below).
All other cases fall back to local compilation.

`nocc-server` is a background process running on every compilation node. 
It handles compilation, stores caches, and writes statistics. 

When `nocc-daemon` starts, it connects to all servers enumerated in the `NOCC_SERVERS` env var. 
For a server, a launched daemon is **a client**, with a unique *clientID* 
([see](./configuration.md#configuring-nocc-client) `NOCC_CLIENT_ID`). 

Here's the order: a build process starts → a daemon starts → a new client appears an all servers → the client uploads files and command-lines → the server compiles them and sends objs back → `nocc` processes start and die, whereas `nocc-daemon` stays in the background → a build process finishes → a daemon dies → the client disappears on all servers.


<p><br></p>

## Balancing files over servers

When a daemon has an invocation to compile `1.cpp`, it **chooses a remote server based on a file name hash** (not a full path, just by basename).
It does not try to balance servers by load, or least used, etc. — just a name hash.

The intention is simple: when a build process runs from different machines, it could be in different folders in CI build agents — we want a file with its dependencies to point to one and the same server always.
Even if file contents have changed since the previous run, probably its dependencies remain more or less the same and thus have already been uploaded to that exact server.

If a remote server is unavailable, a daemon does not try to compile this file on another server: it switches to local compilation. 
The "unavailable" state should be detected and fixed by some external monitoring, we don't want to pollute caches on other servers at this time.


<p><br></p>

## Algorithm and protocol

<p align="center">
    <img src="img/nocc-final-algorithm.drawio.png" alt="algorithm" height="375">
</p>

Here's what a cpp compilation (one `nocc` invocation handled by a daemon) looks like:
* For an input cpp file, find all dependent h/hxx/inc/pch/etc. that are required for compilation.
* Send sha256 of the cpp and all dependencies to the remote. The remote returns indexes that are missing.
* Send all files needed to be uploaded. If all files exist in the remote cache, this step is skipped.
* After the remote receives all required files, it starts compiling obj (or immediately takes it from obj cache).
* When an obj file is ready, the remote pushes it via grpc stream. On a compilation, just *exitCode/stdout/stderr* are sent.
* The daemon saves the .o file, and the `nocc` process dies.


<p><br></p>

## Client file structure mappings

Every running daemon is supposed to have a unique *clientID*. 
All files uploaded from that daemon are saved into a working dir representing a client's file structure. 
The target idea for `nocc-server` is to launch `g++` having prefixed all paths:

<p align="center">
    <img src="img/nocc-file-mapping.drawio.png" alt="file mapping" height="344">
</p>

When a cpp depends on system headers (`<iostream>` and others), they are also checked recursively, 
but a server responds to upload only files that are missing or different. 

While a daemon is running, that directory on a server is populated with files required for compilation 
(either uploaded or hard-linked from src cache, see below). 
When a daemon dies (a client disconnects), the server directory is totally cleared.

Note, that a client working dir *does not contain all files* from a client: only files uploaded to the current shard.
Having 3 servers, a client balances between them based on a cpp basename.


<p><br></p>

## Src cache

**If a file was uploaded once, it isn't required to be uploaded again** — it's the idea behind src cache.

Src cache is based on file hashes (SHA256), not on file names.
It stores source files: cpp, h, hxx, inc, etc.
Files `1.cpp` and `2.cpp` are considered the same if they have equal hashes.

"Restoring from cache" is just a hard link from a storage to a destination path. 
For example, if `1.cpp` and `1.h` were already uploaded:

<p align="center">
    <img src="img/nocc-src-cache.drawio.png" alt="src cache" height="211">
</p>

If `1.cpp` was uploaded, then modified, then its hash would change, and it would be requested to be uploaded again. BTW, after reverting, no uploads will be required, since a previous copy would already exist unless removed.

There is an LRU replacement policy to ensure that a cache folder fits the desired size,
see [configuring nocc-server](./configuration.md#configuring-nocc-server).

All caches are cleared on server restart.


<p><br></p>

## Obj cache

**If a file was compiled once, it isn't required to be compiled again** — it's the idea behind obj cache.

This is especially useful to share obj files across build agents: if one build agent compiles the master branch, other build agents can reuse a ready obj for every cpp.

The hardest problem is how to detect that *"this .cpp was already compiled, we can use .o"*. 
It's also based on hashes.

The final server cmd line looks like
```bash
g++ -Wall -c ... -iquote /tmp/client1/headers -o /tmp/client1/some.cpp.o /tmp/client1/some.cpp
```

We want to reuse a ready obj file if and only if:
* the cpp file is the same (its name and sha256)
* all dependent h/inc/pch/etc. are the same (their count, order, size, sha256)
* all C++ compiler options are the same (except include paths)

<p align="center">
    <img src="img/nocc-obj-cache.drawio.png" alt="obj cache" height="211">
</p>

If a project is being compiled with different compiler options (for example, with and without debug symbols), then every cpp would have two objects stored in obj cached, and recompilation would choose one of them based on the current invocation.

If there were compilation warnings (stderr is not empty), a file is not put to obj cache, just in case.

Like src cache, obj cache also has an LRU expiration. Obj cache is also dropped on restart.


<p><br></p>

## Own includes parser

For every cpp file compiled, we need to detect dependencies (all `#include` recursively).

The standard way to do this is to use the `-M` [flag](https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html): 
it launches a preprocessor (not a compilation) and outputs dependencies of a cpp file (not a preprocessor result).

Own includes parser does the same work as `cxx -M` but much faster.
It has methods that parse cpp/h files, find `#include`, resolve them, and keep going recursively.
It takes all `-I` / `-iquote` / `-isystem` dirs from cmd line into account, it works well with `#include_next`.
A daemon has an includes cache for all invocations, so that system headers are traversed only once.
As a result, we have all dependencies, just like the C++ preprocessor was invoked.

Unlike `cxx -M`, this is not a preprocessor, so it does nothing about `#ifdef` etc.
Hence, it can find more includes than natively, some of them may not exist, especially in system headers.
This is not an error, because, in practice, they are likely to be surrounded with `#ifdef` and never reached by a real C++ compiler.
But if own includes parsed finds fewer dependencies than `cxx -M`, it's a bug.

Along with finding dependencies, hashes are calculated to be sent to a server.

Own includes can work **only if paths are statically resolved**: it can do nothing about `#include MACRO()`.
For instance, it can't analyze boost, as it's full of macro-includes.
Only disabling own includes (invoking a real preprocessor) can help in that case. 
This can be done by setting the `NOCC_DISABLE_OWN_INCLUDES=1` environment variable.


<p><br></p>

## Own precompiled headers

`nocc` provides a custom solution when it comes to precompiled headers. When invoked like
```bash
nocc g++ -x c++-header -o all-headers.h.gch all-headers.h
```
It emits `all-headers.h.nocc-pch` **INSTEAD OF** `.gch/.pch` on a client-side —
and compiled on a server-side into a real `.gch/.pch`.

<p align="center">
    <img src="img/nocc-own-pch.drawio.png" alt="own pch" height="281">
</p>

There are two notable reasons of heading this way:
1. If we compile `.gch` locally, we nevertheless should upload it to all remotes. But `.gch` files are very big, so the first run uploading it to N servers simultaneously takes too long.
2. If `.gch` headers (g++) can work after uploading, `.pch` headers (clang) can not. Clang **won't use a precompiled header** compiled on another machine, even with `--relocatable-pch` flag. The only solution for clang is to compile pch on a remote, anyway.

A `.nocc-pch` file is a text file containing all dependencies required to be compiled on any remote. 
Producing it on a client-side takes noticeably less time than compiling a real pch.

When a client collects dependencies and sees `#include "all-headers.h"`, it discovers `all-headers.h.nocc-pch`
and uploads it like a regular dependency (then `all-headers.h` itself is not uploaded at all).

When `all-headers.h.nocc-pch` is uploaded, the remote compiles it,
resulting in `all-headers.h` and `all-headers.h.gch` again, but stored on remote (until restart).
After it has been uploaded and compiled once, all other cpp files depending on this `.nocc-pch`
will use already compiled `.gch` that is hard-linked into a client working dir.

The original `.gch/.pch` on a client-side is NOT generated, because it's useless if everything works ok.
If remote compilation fails for any reason, `nocc` will fall back to local compilation.
In this case, local compilation will be done without precompiled header, as it doesn't exist.


<p><br></p>

## CMake depfiles

CMake (sometimes with `make`, often with `ninja`) invokes the C++ compiler like
```bash
nocc g++ -MD -MT example.dir/1.cpp.o -MF example.dir/1.cpp.o.d -o example.dir/1.cpp.o -c 1.cpp
```

The [flags](https://gcc.gnu.org/onlinedocs/gcc/Preprocessor-Options.html) 
`-MD` and others mean: along with an object file `1.cpp.o`, generate a dependency file `1.cpp.o.d`.
A dependency file is a text file with all dependent includes found at any depth.
Probably, it's used by CMake to track the recompilation tree on that files change.

`nocc` detects options like `-MD` and emits a depfile on a client-side, after having collected all includes.
Moreover, these options are stripped off and are not sent to the remote at all.

The following options are supported: `-MF {file}`, `-MT {target}`, `-MQ {target}`, `-MD`.  
Others (`-M`/`-MMD`/etc.) are unsupported. When they occur, `nocc` falls back to local compilation.


<p><br></p>

## Local fallback queue

When some remotes are not available, files that were calculated to be compiled on that remotes,
fall back to local compilation.

"Local compilation" is just executing the specified command-line in a separate process.
Note, that local compilation is performed within a daemon instead of passing it to a `nocc` wrapper.
This is done in order to maintain a single queue:
it makes a huge bunch of `nocc` invocations to be throttled to a limited number of local C++ processes.

The local compilation is also launched when a command-line is unsupported or could not be parsed.


# nocc configuration

This page describes how to set up `nocc` and `nocc-server` for production.


<p><br></p>

## Configuring nocc client

All configuration on a client-side is done using environment variables. 
That's because `nocc` is by design invoked without self command-line arguments.

| Env variable                     | Description                                                                                                                                                                                                                                                                                           |
|----------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `NOCC_GO_EXECUTABLE` string      | `/path/to/nocc-daemon` (it's invoked from `nocc`, which is a tiny C++ wrapper).                                                                                                                                                                                                                       |
| `NOCC_CLIENT_ID` string          | This is a *clientID* sent to all servers when a daemon starts. Setting a sensible value makes server logs much more readable. For CI, you can set this to *b{BUILD_ID}*. For developers containers, you can set this to *"dev-{USERNAME}"*. If not set, a random string is generated on daemon start. |
| `NOCC_SERVERS` string            | Remote nocc servers — a list of 'host:port' delimited by ';'. If not set, `nocc` will read `NOCC_SERVERS_FILENAME`.                                                                                                                                                                                   |
| `NOCC_SERVERS_FILENAME` string   | A file with nocc servers — a list of 'host:port', one per line (with optional comments starting with '#'). Used if `NOCC_SERVERS` is unset.                                                                                                                                                           |
| `NOCC_LOG_FILENAME` string       | A filename to log, nothing by default. Errors are duplicated to stderr always.                                                                                                                                                                                                                        |
| `NOCC_LOG_VERBOSITY` int         | Logger verbosity level for INFO (-1 off, default 0, max 2). Errors are logged always.                                                                                                                                                                                                                 |
| `NOCC_DISABLE_OBJ_CACHE` bool    | Disable obj cache on remote: obj will be compiled always and won't be stored.                                                                                                                                                                                                                         |
| `NOCC_DISABLE_OWN_INCLUDES` bool | Disable [own includes parser](./architecture.md#own-includes-parser): use a C++ preprocessor instead. It's much slower, but 100% works. By default, nocc traverses `#include` recursively using its own built-in parser.                                                                              | 
| `NOCC_LOCAL_CXX_QUEUE_SIZE` int  | Amount of parallel processes when remotes aren't available and cxx is launched locally. By default, it's the number of CPUs on the current machine.                                                                                                                                                   |

For real usage, you'll definitely have to specify `NOCC_GO_EXECUTABLE` and `NOCC_SERVERS`. It also makes sense of setting `NOCC_CLIENT_ID` and `NOCC_LOG_FILENAME`. Other options are unlikely to be used. 

When you launch lots of jobs like `make -j 600`, then `nocc-daemon` has to maintain lots of local connections and files at the same time. If you face a "too many open files" error, consider increasing `ulimit -n`.


<p><br></p>

## Configuring nocc server

All configuration on a server-side is done using command-line arguments.
For a server, they are more reliable than environment variables.

| Cmd argument              | Description                                                                             |
|---------------------------|-----------------------------------------------------------------------------------------|
| `-host {string}`          | Binding address, default 0.0.0.0.                                                       |
| `-port {int}`             | Listening port, default 43210.                                                          |
| `-cpp-dir {string}`       | Directory for incoming C++ files and src cache, default */tmp/nocc/cpp*.                |
| `-obj-dir {string}`       | Directory for resulting obj files and obj cache, default */tmp/nocc/obj*.               |
| `-log-filename {string}`  | A filename to log, by default use stderr.                                               |
| `-log-verbosity {int}`    | Logger verbosity level for INFO (-1 off, default 0, max 2). Errors are logged always.   |
| `-src-cache-limit {int}`  | Header and source cache limit, in bytes, default 4G.                                    |
| `-obj-cache-limit {int}`  | Compiled obj cache limit, in bytes, default 16G.                                        |
| `-statsd {string}`        | Statsd udp address (host:port), omitted by default. If omitted, stats won't be written. |
| `-max-parallel-cxx {int}` | Max amount of C++ compiler processes launched in parallel, default *nCPU*.              |

All file caches are lost on restart, as references to files are kept in memory. 
There is also an LRU expiration mechanism to fit cache limits.

When `nocc-server` restarts, it ensures that *working-dir* is empty. 
If not, it's renamed to *working-dir.old*. 
If *working-dir.old* already exists, it's removed recursively.
That's why restarting can take a noticable time if there were lots of files saved in working dir by a previous run.


<p><br></p>

## Server log rotation

When a `nocc-server` process receives the `SIGUSR1` signal, it reopens the specified `-log-filename` again.


<p><br></p>

## Server statistics

If the `-statsd` option is set, `nocc-server` dumps all execution statistics to [statsd](https://github.com/statsd/statsd), and thus can be easily visualized by Grafana. 

The statistics contain all metrics from server startup till now. 
All of them are "gauge", incremented directly. 
In grafana, to view deltas instead of rising metrics, one should use *nonNegativeDerivative*.

A list of all written stats could be obtained [inside statsd.go](../internal/server/statsd.go), see the `fillBufferWithStats()` function. 
They are quite intuitive, that's why we don't duplicate them here. 


<p><br></p>

## Configuring nocc + tmpfs

The directory passed as `-cpp-dir` can be placed in **tmpfs**. 
All operations with cpp files are performed in that directory: 
* incoming files (h/cpp/etc.) are saved there mirroring client's file structure;
* src-cache is placed there;
* pch files are placed there;
* tmp files for preventing race conditions are also there, not in sys tmp dir.

So, if that directory is placed in tmpfs, the C++ compiler will take all files from memory (except for system headers),
which noticeably speeds up compilation.

When setting up limits to tmpfs in a system, ensure that it will fit `-src-cache-limit` plus some extra space.

Note, that placing `-obj-dir` in tmpfs is not recommended, because obj files are usually much heavier,
and they are just transparently streamed back from a hard disk in chunks.


<p><br></p>

## Other commands from a client

`nocc` has some commands aside from the `nocc cxx cmd-line` format:

* `nocc -version` / `nocc -v` — show version and exit
* `nocc -checks-servers` — print out servers status and exit
* `nocc -dump-server-logs` — dump logs from all servers to */tmp/nocc-dump-logs/* and exit; servers must be launched with the `-log-filename` option
* `nocc -drop-server-caches` — drop src cache and obj cache on all servers and exit


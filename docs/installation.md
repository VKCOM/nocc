# Installing nocc

Here we'll install `nocc` and launch a sample cpp file to make sure everything works on a local machine.


<p><br></p>

## Installing nocc from ready binaries

`nocc` project consists of three binaries:

* `nocc` — a tiny C++ wrapper
* `nocc-daemon` — a daemon that would run in the background during the build process
* `nocc-server` — a binary to be run on the server-side

The easiest way to install is just to download binaries from the [releases page](https://github.com/VKCOM/nocc/releases).

After extracting an archive with `tar -xvf nocc-xxx.tar.gz`, you'll get these 3 binaries.

*Note, that for Mac, you'll probably have a "developer cannot be verified" warning. It can be suppressed in Security settings. Anyway, running `nocc` for mac is just for development/testing purposes.*


<p><br></p>

## Installing nocc from sources

You'll need Go (1.16 or higher) and g++ to be installed (on Mac, `g++` is usually a symlink to `clang++`, that's okay).

Clone this repo, proceed to its root, and run:

```bash
make client
make server
```

You'll have 3 binaries emitted in the `bin/` folder.

*Note, that for modifying source code, you'll also have to install protobuf compiler,
see [bootstrap.sh](../bootstrap.sh)*


<p><br></p>

## Run a simple example locally

Save this fragment as `1.cpp`:

```cpp
#include "1.h"

int square(int a) { 
  return a * a; 
}
```

And this one — as `1.h`:

```cpp
int square(int a);
```

Run `./nocc-server`. You'll see a message like

```text
INFO nocc-server started, listening to 0.0.0.0:43210
```

Open another console: you'll run `nocc` client there. 
As a minimal requirement, you'll have to specify `NOCC_SERVERS` and `NOCC_GO_EXECUTABLE` environment variables:

```bash
NOCC_SERVERS=127.0.0.1:43210 NOCC_GO_EXECUTABLE=/path/to/nocc-daemon /path/to/nocc g++ 1.cpp -o 1.o -c
```

You'll see a warning, that daemon logs won't be available, skip it. 
If everything works, there should be `1.o` emitted.
To make sure that it's not just a local launch, look through server logs in the console (about a new client and so on).


<p><br></p>

## Configuration

Of course, launching `nocc-server` locally is useless. 
To have a performance impact, you should have multiple compilation servers with `nocc-server` running with proper options.

Proceed to the [configuration page](./configuration.md) for these details.

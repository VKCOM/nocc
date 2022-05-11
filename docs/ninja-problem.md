# nocc with ninja problem (and a workaround)

If you use `ninja` instead of `make` on a huge project with `nocc`, you would probably face an annoying problem.

As you know, the first `nocc` invocation starts `nocc-daemon`. A daemon dies in 15 seconds after `nocc` stops connecting (it's a heuristic that a build process has finished). 

Whyever, building with `ninja` behaves like this:
```
# ninja -j 200
nocc g++ 1.cpp ...    <- starts a daemon
nocc g++ 2.cpp ...
...
nocc g++ 200.cpp ...  <- 200 parallel jobs

# !!! whyever, ninja hangs, and continues only after a daemon quits
nocc g++ 201.cpp ...  <- starts a daemon again
...
nocc g++ 400.cpp      <- 200 more parallel jobs

# ninja waits for a daemon to quit again, and so on
```

I don't know the actual reason. There is no similar problem with `make`. Probably, `ninja` waits not only for direct child processes (a bunch of `nocc`) but also for their children (`nocc-daemon` launched by the first `nocc`).

Anyway, there is a workaround: start `nocc-daemon` manually in advance, so that all `nocc` launched by `ninja` would connect to it.

A command-line for starting a daemon is (the same as the first `nocc` does):
```bash
/path/to/nocc start
```

and then launch your build:
```bash
ninja -j 200
```

If you find an easier workaround, or if you don't encounter such a problem at all, please, let me know.

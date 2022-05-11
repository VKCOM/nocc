/*
  `nocc` is a C++ lightweight binary that pipes command-line to `nocc-daemon`.
  When a build system (cmake / KPHP / etc.) simultaneously calls compilation jobs like
    > nocc g++ ... 1.cpp
    > nocc g++ ... 2.cpp
    > other 100k jobs
  then this binary is executed actually.
  What it does:
  1) The very first `nocc` invocation starts `nocc-daemon`:
     a daemon serves grpc connections to nocc servers and actually does all stuff for remote compilation.
  2) Every `nocc` invocation pipes command-line (g++ ...) to a daemon via unix socket,
     a daemon compiles it remotely and writes the resulting .o file, then `nocc` process dies.
  3) `nocc` jobs start and die: a build system executes and balances them.
  4) `nocc-daemon` dies in 15 seconds after `nocc` stops connecting (after the compilation process finishes).
 */

#include <sys/file.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <errno.h>
#include <time.h>

const char *LOCKFILE = "/tmp/nocc.lock";    // an inter-process lockfile to launch a daemon only once
const char *UNIX_SOCK = "/tmp/nocc.sock";   // hardcoded in a daemon also
const char *NOCC_GO_EXECUTABLE;             // env var

int ARGC;
char **ARGV;

const int BUF_PIPE_LEN = 32768;
char BUF_PIPE[BUF_PIPE_LEN]; // a single buffer for in/out communication with nocc-daemon

struct GoDaemonResponse {
  int ExitCode{0};
  char *Stdout{nullptr};
  char *Stderr{nullptr};
};

char *format_time_to_log() {
  static char time_buf[64];
  time_t ts = time(nullptr);
  tm *now = localtime(&ts);
  sprintf(time_buf, "%d/%02d/%02d %02d:%02d:%02d", 1900 + now->tm_year, 1 + now->tm_mon, now->tm_mday, now->tm_hour, now->tm_min, now->tm_sec);
  return time_buf;
}

void append_message_to_log_file(const char *msg) {
  const char *filename = getenv("NOCC_LOG_FILENAME");
  if (filename) {
    FILE *f = fopen(filename, "a");
    if (f != nullptr) {
      fprintf(f, "%s INFO %s\n", format_time_to_log(), msg);
      fclose(f);
    }
  }
}

void append_error_to_log_file(const char *errToPrint, int errnum) {
  const char *filename = getenv("NOCC_LOG_FILENAME");
  if (filename) {
    FILE *f = fopen(filename, "a");
    if (f != nullptr) {
      if (errnum) {
        fprintf(f, "%s ERROR %s: %s (fallback to local cxx)\n", format_time_to_log(), errToPrint, strerror(errnum));
      } else {
        fprintf(f, "%s ERROR %s (fallback to local cxx)\n", format_time_to_log(), errToPrint);
      }
      fclose(f);
    }
  }
}

void append_error_to_stderr(const char *errToPrint, int errnum) {
  if (errnum) {
    fprintf(stderr, "[nocc] %s: %s. Executing the C++ compiler locally...\n", errToPrint, strerror(errnum));
  } else {
    fprintf(stderr, "[nocc] %s. Executing the C++ compiler locally...\n", errToPrint);
  }
}

// execute_cxx_locally() replaces current process (nocc.cpp) with a cxx process
// it's called when a daemon is unavailable or for linking
// (for linking, nocc.cpp doesn't send a command to a daemon, as an optimization)
// note, that if remote compilation fails,
// a daemon falls back to local cxx within itself, in order to maintain a local compilation queue
void __attribute__((noreturn)) execute_cxx_locally(const char *errToPrint, int errnum = 0) {
  if (errToPrint) {
    append_error_to_log_file(errToPrint, errnum);
    append_error_to_stderr(errToPrint, errnum);
  }
  execvp(ARGV[1], ARGV + 1);
  printf("could not run %s, exit(1)\n", ARGV[1]);
  exit(1);
}

void __attribute__((noreturn)) execute_distcc_locally() {
  ARGV[0] = strdup("distcc");
  execvp("distcc", ARGV + 0);
  printf("could not run `distcc`, exit(1)\n");
  exit(1);
}

void __attribute__((noreturn)) execute_go_nocc_instead_of_cpp() {
  execv(NOCC_GO_EXECUTABLE, ARGV);
  printf("could not run %s, exit(1)\n", NOCC_GO_EXECUTABLE);
  exit(1);
}

// the very first `nocc` invocation starts `nocc-daemon` in a separate process
// we start the process and wait for something in stdout
// it will be either an error message (if a daemon failed to start) or "1"
// after a daemon starts, we'll connect to it in a regular way
void start_daemon_in_background() {
  // when multiple `nocc` are launched simultaneously, let only the first process reaching this point start a daemon
  // others will sleep; they will wake up after a daemon has been started
  // this is done via lockfile
  int lockfd = open(LOCKFILE, O_RDWR | O_CREAT, 0666);
  if (flock(lockfd, LOCK_EX | LOCK_NB)) {   // another process is being creating a daemon
    flock(lockfd, LOCK_EX);                 // unblock when that process finishes creating a daemon
    close(lockfd);
    return;
  }
  // this is the first and the only process creating a daemon

  const char *log_filename = getenv("NOCC_LOG_FILENAME");
  if (log_filename) {
    fprintf(stderr, "[nocc] starting daemon, see logs in %s\n", log_filename);
  } else {
    fprintf(stderr, "[nocc] starting daemon; warning! env NOCC_LOG_FILENAME not set, logs won't be available\n");
  }

  int pipd[2];
  pipe(pipd);

  int pid = fork();
  if (pid < 0) {
    execute_cxx_locally("could not start daemon", errno);
  }
  // child process: replace with `nocc-daemon start`
  if (pid == 0) {
    dup2(pipd[1], STDOUT_FILENO);
    close(pipd[0]);
    execl(NOCC_GO_EXECUTABLE, NOCC_GO_EXECUTABLE, "start", nullptr);
    exit(1);
  }
  // main process: wait for stdout (go daemon prints there upon init)
  // on success, it writes "1"; on error, an error message to print out
  close(pipd[1]);

  ssize_t n_read = read(pipd[0], BUF_PIPE, 1000);
  if (n_read <= 0) {
    execute_cxx_locally("could not start daemon", errno);
  }
  if (BUF_PIPE[0] != '1' || BUF_PIPE[1] != '\0') {
    execute_cxx_locally(BUF_PIPE);
  }

  unlink(LOCKFILE);
  flock(lockfd, LOCK_UN);
  close(lockfd);
}

// connect to currently running `nocc-daemon` or start a new one, if it's the very first `nocc` invocation
int connect_to_go_daemon_or_start_a_new_one() {
  sockaddr_un saddr{.sun_family=AF_UNIX};
  strcpy(saddr.sun_path, UNIX_SOCK);
  int sockfd = socket(AF_UNIX, SOCK_STREAM, 0);

  if (connect(sockfd, (sockaddr *)&saddr, sizeof(saddr)) == 0) {
    return sockfd;
  }

  start_daemon_in_background();
  if (connect(sockfd, (sockaddr *)&saddr, sizeof(saddr)) == 0) {
    return sockfd;
  }
  return -1;
}

// pipe current command-line invocation to a daemon via unix socket
// request message format:
// "{Cwd} {CmdLine...}\0"
// see daemon-sock.go, onRequest()
void write_request_to_go_daemon(int sockfd) {
  if (!getcwd(BUF_PIPE, BUF_PIPE_LEN - 2)) {
    execute_cxx_locally("getcwd failed", errno);
  }
  size_t len = strlen(BUF_PIPE);
  BUF_PIPE[len++] = '\b';

  for (int i = 1; i < ARGC; ++i) {
    size_t end = len + strlen(ARGV[i]);
    if (end > BUF_PIPE_LEN - 1) {
      fprintf(stderr, "too long %d: %s", ARGC, BUF_PIPE);
      execute_cxx_locally("too long command-line invocation");
    }
    strcpy(BUF_PIPE + len, ARGV[i]);
    len = end;
    BUF_PIPE[len++] = '\b'; // a delimiter between argv that can't occur in an ordinary text
  }
  BUF_PIPE[--len] = '\0';

  if (len + 1 != send(sockfd, BUF_PIPE, len + 1, 0)) {
    execute_cxx_locally("could not write to daemon socket", errno);
  }
}

// read a response from a daemon
// reading will block until a daemon responses: only then it writes back to socket
// response message format:
// "{ExitCode}\0{Stdout}\0{Stderr}\0"
// if remote compilation fails, it falls back to local compilation within a daemon,
// so a daemon always responds in such a format
// see daemon-sock.go, onRequest()
GoDaemonResponse read_response_from_go_daemon(int sockfd) {
  ssize_t len = recv(sockfd, BUF_PIPE, sizeof(BUF_PIPE), 0);
  if (len <= 0) {
    execute_cxx_locally("could not read from daemon socket", errno);
  }
  if (len == sizeof(BUF_PIPE)) {
    // this could be handled properly by dynamic buffers
    execute_cxx_locally("todo too big output from go");
  }

  GoDaemonResponse output;
  char *end;
  output.ExitCode = static_cast<int>(strtol(BUF_PIPE, &end, 10));
  if (end == BUF_PIPE || *end != '\0') {
    execute_cxx_locally("could not parse daemon response");
  }
  output.Stdout = end + 1;
  output.Stderr = output.Stdout + strlen(output.Stdout) + 1;
  return output;
}

// heuristics, if current invocation is called for linking: `nocc g++ 1.o 2.o -o bin/o`
// then we'll bypass requesting a daemon
// it's a moment of optimization, since such command-lines are usually long
bool is_called_for_linking() {
  int in_o_count = 0;
  for (int i = 0; i < ARGC; ++i) {
    const char *arg = ARGV[i];
    size_t l = strlen(arg);

    if (arg[0] == '-' || l < 4) {
      // handle -o {out} (if .so, it's linking, otherwise just skip this argument)
      if (arg[0] == '-' && arg[1] == 'o' && arg[2] == 0 && i < ARGC-1) {
        const char *out = ARGV[i+1];
        size_t l = strlen(out);
        if (l > 4 && out[l - 3] == '.' && out[l - 2] == 's' && out[l - 1] == 'o') {
          return true;
        }
        i++;
      }
      continue;
    }

    if (arg[l - 2] == '.' && arg[l - 1] == 'o' ||
        arg[l - 2] == '.' && arg[l - 1] == 'a' ||
        arg[l - 3] == '.' && arg[l - 2] == 's' && arg[l - 1] == 'o') {
      in_o_count++;
    }
  }
  return in_o_count > 1;
}


int main(int argc, char *argv[]) {
  ARGC = argc;
  ARGV = argv;

  NOCC_GO_EXECUTABLE = getenv("NOCC_GO_EXECUTABLE");
  if (NOCC_GO_EXECUTABLE == nullptr) {
    fprintf(stderr, "Error: to make `nocc` run, set NOCC_GO_EXECUTABLE=/path/to/nocc-daemon env variable\n");
    exit(1);
  }

  // this possible fallback will be available for some time just in case
  char *env_fallback_to_distcc = getenv("NOCC_FALLBACK_TO_DISTCC");
  bool fallback_to_distcc = env_fallback_to_distcc != nullptr && env_fallback_to_distcc[0] == '1';
  if (fallback_to_distcc) {
    execute_distcc_locally();
  }

  if (ARGC == 2 && !strcmp(ARGV[1], "start")) {
    int sockfd = connect_to_go_daemon_or_start_a_new_one();
    exit(sockfd == -1 ? 1 : 0);
  }
  if (ARGC < 3 || ARGV[1] && ARGV[1][0] == '-') {
    execute_go_nocc_instead_of_cpp();
  }
  if (ARGC > 4 && is_called_for_linking()) {
    append_message_to_log_file("will execute linking locally");
    execute_cxx_locally(nullptr);
  }

  int sockfd = connect_to_go_daemon_or_start_a_new_one();
  if (sockfd == -1) {
    execute_cxx_locally("could not connect to daemon after starting");
  }
  write_request_to_go_daemon(sockfd);

  GoDaemonResponse response = read_response_from_go_daemon(sockfd);

  fwrite(response.Stdout, strlen(response.Stdout), 1, stdout);
  fwrite(response.Stderr, strlen(response.Stderr), 1, stderr);
  return response.ExitCode;
}


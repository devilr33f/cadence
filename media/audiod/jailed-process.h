#pragma once

#include "audiod/log.h"
#include "audiod/soloist-jail.h"
#include "audiod/sys.h"
#include "result.h"

#include <sys/types.h>

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::audiod {

// a probe's bounds (reference §6.2): soloist --version answers within them
inline constexpr std::chrono::milliseconds kCaptureLimit = std::chrono::seconds(10);
inline constexpr std::size_t kCaptureBytes = 4096;
// how long a killed program may take to be reaped before the wait is left to its thread (reference §7)
inline constexpr std::chrono::milliseconds kReapWait = std::chrono::seconds(5);

// how a jailed program ended
struct JailedExit {
  // its exit status, or -1 when a signal ended it
  int code = -1;
  // the signal that ended it, else 0
  int signal = 0;
};

// why a jailed program did not start, as logged: jail_refused reason=… mount=…, or jail_spawn_failed step=… errno=…
// (mount=… for a mount step)
struct JailError {
  // the plan was refused before the fork
  bool refused = false;
  // refused: symlink|missing|kind; else the step that failed: one of the child's (jailStepName), or the parent's own
  // pipe, open, fork, thread
  std::string_view reason;
  std::string_view mount;
  int code = 0;
};

// a program run unjailed (airplay's daemons, M4d-D2): as audiod's own user, with no namespace, chroot or id change,
// through the rest of a jail's child sequence: its signals reset, nice and the oom score, no new privileges, the death
// signal and the parent check, every descriptor of audiod's closed at exec, then program with exactly env. stdin is
// /dev/null; stdout and stderr are two pipes the owner reads (takeOutput, takeErrors) with output, else /dev/null too,
// what it prints dropped unread
struct PlainPlan {
  std::string program;
  std::vector<std::string> env;
  int nice = 0;
  std::string oomScoreAdj;
  bool output = false;
};

// what a capture got from a program's stdout
struct Captured {
  std::string output;
  // nullopt when it was not reaped within kReapWait of its kill
  std::optional<JailedExit> exit;
  // the limit passed before its stdout closed and it exited, and it was killed
  bool timedOut = false;
  // it wrote more than the bound, and was killed
  bool truncated = false;
};

// one program run in soloist's jail (soloist-jail), owned for its life. a thread of its own forks it and stays until
// the program is reaped: the death signal fires when the thread that forked exits, so it has to live as long as the
// program. stdin is /dev/null, stdout and stderr two pipes the owner reads (soloist's lines carry track and user
// names: they are never logged locally, M6b-D7). arguments may hold a secret, so they are never logged
class JailedProcess {
 public:
  // called once on the process's own thread with the reaped exit: the owner posts it to its loop, and neither blocks
  // nor calls back into the process from it. it never comes after a failed start or once destruction began
  using ExitCallback = std::function<void(JailedExit)>;

  // checks the plan (jailed builds), forks and waits until the program was exec'd or a step failed; the error is
  // logged
  static Result<std::unique_ptr<JailedProcess>, JailError> start(const Log& log, const JailPlan& plan,
                                                                 std::vector<std::string> args, ExitCallback onExit);
  // as start, unjailed in every build; its failure is logged as process_spawn_failed step=… errno=…
  static Result<std::unique_ptr<JailedProcess>, JailError> startPlain(const Log& log, const PlainPlan& plan,
                                                                      std::vector<std::string> args,
                                                                      ExitCallback onExit);

  // runs the program to its end with stdout captured, stderr on /dev/null (crashpad's lines), up to maxBytes and
  // within limit; a program still running then is killed. blocks the caller for at most limit and kReapWait
  static Result<Captured, JailError> runCapture(const Log& log, const JailPlan& plan, std::vector<std::string> args,
                                                std::chrono::milliseconds limit = kCaptureLimit,
                                                std::size_t maxBytes = kCaptureBytes);
  // as runCapture, unjailed in every build (a plain soloist's --version through the runtime's loader)
  static Result<Captured, JailError> runCapturePlain(const Log& log, const PlainPlan& plan,
                                                     std::vector<std::string> args,
                                                     std::chrono::milliseconds limit = kCaptureLimit,
                                                     std::size_t maxBytes = kCaptureBytes);

  // kills a running program and waits up to kReapWait for it, unless a stop or a capture already waited that long in
  // vain; past that its thread reaps it alone
  ~JailedProcess();

  JailedProcess(const JailedProcess&) = delete;
  JailedProcess& operator=(const JailedProcess&) = delete;

  [[nodiscard]] pid_t pid() const { return pid_; }
  // without waiting
  [[nodiscard]] bool reaped() const;

  // the read ends of stdout and of stderr, non-blocking and close-on-exec; each valid once, and for a plain one only
  // with its plan's output
  UniqueFd takeOutput();
  UniqueFd takeErrors();

  // no-op once it was reaped, so a pid the kernel gave to another process is never signalled
  void kill(int signo);

  // SIGTERM, then SIGKILL once grace passed; the exit once reaped, nullopt when kReapWait after the kill was not enough
  std::optional<JailedExit> stop(std::chrono::milliseconds grace);

 private:
  struct State;

  JailedProcess(std::shared_ptr<State> state, std::thread thread, UniqueFd output, UniqueFd errors, pid_t pid);

  // where the child's stdout and stderr go
  enum class Streams : std::uint8_t { Separate, StdoutOnly, None };
  // soloist's jail as the build makes it (none in a jail-off build), or none in any build
  enum class Confinement : std::uint8_t { Soloist, None };

  static Result<std::unique_ptr<JailedProcess>, JailError> spawn(const Log& log, const JailPlan& plan,
                                                                 std::vector<std::string> args, ExitCallback onExit,
                                                                 Streams streams, Confinement confinement);
  // the capture both runCapture forms share: its stdout to the end or the limit, then its exit
  static Result<Captured, JailError> captured(Result<std::unique_ptr<JailedProcess>, JailError> started,
                                              std::chrono::steady_clock::time_point deadline, std::size_t maxBytes);

  // the exit once reaped by deadline
  std::optional<JailedExit> waitUntil(std::chrono::steady_clock::time_point deadline);

  std::shared_ptr<State> state_;
  std::thread thread_;
  UniqueFd output_;
  UniqueFd errors_;
  pid_t pid_;
  // a wait for the reap after SIGKILL already ran out: the destructor does not wait again
  bool reapTimedOut_ = false;
};

}  // namespace cadence::audiod

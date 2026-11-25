#pragma once

#include <signal.h>
#include <sys/types.h>

#include <cstdint>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

#ifndef CADENCE_MEDIA_SOLOIST_JAIL
#error "CADENCE_MEDIA_SOLOIST_JAIL must be defined as 0 or 1"
#endif

namespace cadence::audiod {

// a device build always jails soloist; only a sim build may run it unjailed (M4c-D4), with no switch at run time
inline constexpr bool kSoloistJailed = CADENCE_MEDIA_SOLOIST_JAIL != 0;

// v1's helper ran at these, and soloist with it (reference §1.4): soloist is the first thing to give up under
// pressure, below audiod in cpu and first for the oom killer. a plain (unjailed) launch takes the same
inline constexpr int kSoloistNice = 5;
inline constexpr std::string_view kSoloistOomScoreAdj = "500";

// the host paths soloist's jail is built from
struct JailPaths {
  // the trixie runtime, the jail's root
  std::string runtimeRoot;
  // the soloist binary to run: the installed build, or a candidate for its --version
  std::string build;
  std::string resolvConf;
  std::string caFile;
  // the scratch's home and run (soloist-scratch)
  std::string home;
  std::string run;
};

// one mount(2) of the child's sequence; an empty source or fstype is passed as null
struct JailMount {
  // what it serves, for logs: propagation, root, soloist, dev_null, dev_urandom, dev_zero, proc, resolv_conf, ca,
  // home, run
  std::string_view name;
  std::string source;
  std::string target;
  std::string fstype;
  unsigned long flags = 0;

  bool operator==(const JailMount&) const = default;
};

// what the child does between fork and exec, as data (reference §1.2–§1.4): unshare(unshareFlags), the mounts in
// order, chroot(root) and chdir("/"), nice and oomScoreAdj while still root, setgroups(0), setgid(gid), setuid(uid),
// then exec of program with exactly env. a jail-off build skips the namespace, the chroot and the ids (and
// oomScoreAdj unless root), and execs build instead
struct JailPlan {
  std::string root;
  int unshareFlags = 0;
  std::vector<JailMount> mounts;
  std::string program;
  std::string build;
  std::vector<std::string> env;
  // soloist is the first thing to give up under pressure: below audiod in cpu, first for the oom killer
  int nice = 0;
  std::string oomScoreAdj;
  uid_t uid = 0;
  gid_t gid = 0;
};

// v1's sequence: propagation made private, then each bind followed by the remount a read-only one needs, and a
// fresh proc; every target inside the runtime root
JailPlan planJail(const JailPaths& paths);

// why the host cannot hold a plan's jail: symlink, missing or kind, and the mount it concerns
struct JailRefusal {
  std::string_view reason;
  std::string_view mount;

  bool operator==(const JailRefusal&) const = default;
};

// a mount the child cannot make fails where nothing can say which, so this checks before the fork, in plan order:
// the root is a directory, every source and target exists with no symlink on its path, and each target has its
// source's kind (a directory, else a regular file; proc needs a directory)
std::optional<JailRefusal> checkJail(const JailPlan& plan);

// the child's steps, numbered for the report a failing one writes before it exits
enum class JailStep : std::int32_t {
  Signals = 1,
  Unshare,
  Mount,
  Chroot,
  Chdir,
  Nice,
  OomScore,
  Setgroups,
  Setgid,
  Setuid,
  NoNewPrivs,
  DeathSignal,
  Parent,
  Stdio,
  Cloexec,
  Nonblock,
  Exec,
};

std::string_view jailStepName(std::int32_t step);

// whether parent is still this process's parent: a parent that died before the child armed its death signal never
// sends it. async-signal-safe
bool stillChildOf(pid_t parent) noexcept;

// what a failing step writes to the report pipe: one write, far below PIPE_BUF, so never torn
struct JailReport {
  std::int32_t step = 0;
  // the failed mount's index in the plan, else -1
  std::int32_t mount = -1;
  std::int32_t code = 0;
};

// the child's standard streams and the close-on-exec pipe a failing step reports to; all four at or above 3, so the
// child's dup2 onto 0-2 never closes one of them
struct JailFds {
  int in = -1;
  int out = -1;
  int err = -1;
  int report = -1;
};

// everything the child needs, laid out before the fork: the forked child of a threaded process may only make
// async-signal-safe calls, so it allocates nothing and takes no lock. it points into its own copies, so it never moves.
// soloist passes kSoloistJailed as jailed, fixed at build time; a program that is never jailed (airplay's daemons)
// passes false and skips the steps a jail-off build skips
class JailExec {
 public:
  JailExec(const JailPlan& plan, const std::vector<std::string>& args, JailFds fds, bool jailed);

  JailExec(const JailExec&) = delete;
  JailExec& operator=(const JailExec&) = delete;

  // in the forked child: the plan's sequence, then exec; a failing step reports and exits 127
  [[noreturn]] void enter() const noexcept;

 private:
  struct Mount {
    const char* source;
    const char* target;
    const char* fstype;
    unsigned long flags;
  };

  [[noreturn]] void fail(JailStep step, int mount = -1) const noexcept;
  void lowerPriority() const noexcept;
  void markCloseOnExec() const noexcept;
  void clearNonblocking() const noexcept;

  JailPlan plan_;
  std::vector<std::string> args_;
  std::vector<Mount> mounts_;
  std::vector<char*> argv_;
  std::vector<char*> envp_;
  const char* program_ = nullptr;
  JailFds fds_;
  bool jailed_ = true;
  pid_t parent_ = 0;
  // the oom score only drops back as root, so a jail-off build that is not root leaves it as audiod's
  bool writeOomScore_ = false;
  int fdLimit_ = 0;
  struct sigaction default_ {};
  sigset_t unblocked_{};
};

}  // namespace cadence::audiod

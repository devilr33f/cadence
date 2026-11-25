#include "audiod/soloist-jail.h"

#include <fcntl.h>
#include <grp.h>
#include <sched.h>
#include <sys/mount.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <cerrno>
#include <charconv>
#include <string_view>
#include <system_error>
#include <utility>

namespace cadence::audiod {

namespace {

// nobody: soloist runs with no ids of the host's
constexpr uid_t kNobody = 65534;
constexpr std::string_view kProgram = "/soloist";
constexpr std::string_view kHome = "/home/soloist";
constexpr std::string_view kCaTarget = "/etc/ssl/certs/ca-certificates.crt";
constexpr unsigned long kReadOnly = MS_REMOUNT | MS_BIND | MS_RDONLY;
// the sweep below is a loop of syscalls in the child, so the descriptor range it walks is bounded
constexpr rlim_t kSweepCap = 65536;
constexpr int kExitFailed = 127;
constexpr const char* kOomScorePath = "/proc/self/oom_score_adj";

// by JailStep, from Signals on
constexpr std::array kStepNames{"signals", "unshare", "mount", "chroot", "chdir", "nice", "oom_score", "setgroups",
                                "setgid", "setuid", "no_new_privs", "death_signal", "parent", "stdio", "cloexec",
                                "nonblock", "exec"};
static_assert(kStepNames.size() == static_cast<std::size_t>(JailStep::Exec));

enum class Kind { Directory, File };

// the kind a bind's target must have: its source's, as mount(2) binds a directory only on a directory
Kind kindOf(const struct stat& info) { return S_ISDIR(info.st_mode) ? Kind::Directory : Kind::File; }

bool fits(const struct stat& info, Kind kind) {
  return kind == Kind::Directory ? S_ISDIR(info.st_mode) : S_ISREG(info.st_mode);
}

// lstat of every prefix of an absolute path: a symlink anywhere refuses, an absent part is missing. last is the
// path's own entry
std::optional<std::string_view> walk(const std::string& path, struct stat& last) {
  if (!path.starts_with('/'))
    return "missing";
  for (std::size_t end = 0; end != std::string::npos;) {
    end = path.find('/', end + 1);
    const auto prefix = path.substr(0, end == std::string::npos ? path.size() : std::max<std::size_t>(end, 1));
    if (::lstat(prefix.c_str(), &last) != 0)
      return "missing";
    if (S_ISLNK(last.st_mode))
      return "symlink";
  }
  return std::nullopt;
}

std::optional<JailRefusal> checkMount(const JailMount& mount) {
  const auto refuse = [&](std::string_view reason) { return JailRefusal{.reason = reason, .mount = mount.name}; };
  Kind wanted = Kind::Directory;
  if (mount.fstype.empty()) {
    struct stat source {};
    if (const auto reason = walk(mount.source, source))
      return refuse(*reason);
    wanted = kindOf(source);
  }
  struct stat target {};
  if (const auto reason = walk(mount.target, target))
    return refuse(*reason);
  if (!fits(target, wanted))
    return refuse("kind");
  return std::nullopt;
}

}  // namespace

JailPlan planJail(const JailPaths& paths) {
  const auto& root = paths.runtimeRoot;
  const auto inside = [&](std::string_view target) { return root + std::string(target); };
  JailPlan plan{.root = root,
                .unshareFlags = CLONE_NEWNS,
                .mounts = {},
                .program = std::string(kProgram),
                .build = paths.build,
                .env = {"HOME=" + std::string(kHome), "SPOTIFY_SSL_CERT_FILE=" + std::string(kCaTarget), "PATH=/"},
                .nice = kSoloistNice,
                .oomScoreAdj = std::string(kSoloistOomScoreAdj),
                .uid = kNobody,
                .gid = kNobody};
  auto& mounts = plan.mounts;
  // nothing mounted below may propagate back to the host
  mounts.push_back({.name = "propagation", .source = {}, .target = "/", .fstype = {}, .flags = MS_REC | MS_PRIVATE});
  const auto bind = [&](std::string_view name, const std::string& source, std::string target, bool readOnly) {
    mounts.push_back({.name = name, .source = source, .target = target, .fstype = {}, .flags = MS_BIND});
    // a bind takes its source mount's flags: read-only takes a remount of the bind itself
    if (readOnly)
      mounts.push_back({.name = name, .source = {}, .target = std::move(target), .fstype = {}, .flags = kReadOnly});
  };
  bind("root", root, root, true);
  bind("soloist", paths.build, inside(kProgram), true);
  bind("dev_null", "/dev/null", inside("/dev/null"), false);
  bind("dev_urandom", "/dev/urandom", inside("/dev/urandom"), false);
  bind("dev_zero", "/dev/zero", inside("/dev/zero"), false);
  mounts.push_back({.name = "proc",
                    .source = "proc",
                    .target = inside("/proc"),
                    .fstype = "proc",
                    .flags = MS_NOSUID | MS_NODEV | MS_NOEXEC});
  bind("resolv_conf", paths.resolvConf, inside("/etc/resolv.conf"), true);
  bind("ca", paths.caFile, inside(kCaTarget), true);
  bind("home", paths.home, inside(kHome), false);
  bind("run", paths.run, inside("/run/cadence"), false);
  return plan;
}

std::optional<JailRefusal> checkJail(const JailPlan& plan) {
  struct stat root {};
  if (const auto reason = walk(plan.root, root))
    return JailRefusal{.reason = *reason, .mount = "root"};
  if (!S_ISDIR(root.st_mode))
    return JailRefusal{.reason = "kind", .mount = "root"};
  for (const auto& mount : plan.mounts) {
    // the propagation change and the remounts act on what is mounted already
    const bool bind = (mount.flags & MS_BIND) != 0 && (mount.flags & MS_REMOUNT) == 0;
    if (!bind && mount.fstype.empty())
      continue;
    if (const auto refusal = checkMount(mount))
      return refusal;
  }
  return std::nullopt;
}

std::string_view jailStepName(std::int32_t step) {
  if (step < 1 || static_cast<std::size_t>(step) > kStepNames.size())
    return "report";
  return kStepNames[static_cast<std::size_t>(step - 1)];
}

bool stillChildOf(pid_t parent) noexcept { return ::getppid() == parent; }

namespace {

// whether this process's oom score is below target, read before the fork, as the child starts with the same: one at
// or above it is as killable as asked already, and lowering it takes CAP_SYS_RESOURCE, which a container's root can
// lack (kubernetes puts its pods' processes near 1000)
bool oomScoreBelow(std::string_view target) {
  int wanted = 0;
  if (std::from_chars(target.data(), target.data() + target.size(), wanted).ec != std::errc{})
    return true;
  const int fd = ::open(kOomScorePath, O_RDONLY | O_CLOEXEC);
  if (fd < 0)
    return true;
  std::array<char, 16> text{};
  const auto got = ::read(fd, text.data(), text.size() - 1);
  ::close(fd);
  int current = 0;
  if (got <= 0 || std::from_chars(text.data(), text.data() + got, current).ec != std::errc{})
    return true;
  return current < wanted;
}

}  // namespace

JailExec::JailExec(const JailPlan& plan, const std::vector<std::string>& args, JailFds fds, bool jailed)
    : plan_(plan),
      args_(args),
      fds_(fds),
      jailed_(jailed),
      parent_(::getpid()),
      writeOomScore_(!plan_.oomScoreAdj.empty() && (jailed || ::geteuid() == 0) && oomScoreBelow(plan_.oomScoreAdj)) {
  const auto pointer = [](const std::string& text) { return text.empty() ? nullptr : text.c_str(); };
  for (const auto& mount : plan_.mounts)
    mounts_.push_back({pointer(mount.source), mount.target.c_str(), pointer(mount.fstype), mount.flags});
  auto& program = jailed_ ? plan_.program : plan_.build;
  program_ = program.c_str();
  argv_.push_back(program.data());
  for (auto& arg : args_)
    argv_.push_back(arg.data());
  argv_.push_back(nullptr);
  for (auto& variable : plan_.env)
    envp_.push_back(variable.data());
  envp_.push_back(nullptr);

  rlimit limit{};
  fdLimit_ = static_cast<int>(::getrlimit(RLIMIT_NOFILE, &limit) == 0 ? std::min(limit.rlim_cur, kSweepCap)
                                                                       : kSweepCap);
  default_.sa_handler = SIG_DFL;
  sigemptyset(&default_.sa_mask);
  sigemptyset(&unblocked_);
}

void JailExec::fail(JailStep step, int mount) const noexcept {
  const JailReport report{.step = static_cast<std::int32_t>(step), .mount = mount, .code = errno};
  // nothing to do about a report that cannot be written: the parent then sees the exit without one
  [[maybe_unused]] const auto written = ::write(fds_.report, &report, sizeof report);
  ::_exit(kExitFailed);
}

// setpriority, then open, write and close of a path and bytes laid out before the fork
void JailExec::lowerPriority() const noexcept {
  if (::setpriority(PRIO_PROCESS, 0, plan_.nice) != 0)
    fail(JailStep::Nice);
  if (!writeOomScore_)
    return;
  const int fd = ::open(kOomScorePath, O_WRONLY | O_CLOEXEC);
  if (fd < 0)
    fail(JailStep::OomScore);
  const auto size = plan_.oomScoreAdj.size();
  const bool written = ::write(fd, plan_.oomScoreAdj.data(), size) == static_cast<ssize_t>(size);
  const int code = errno;
  ::close(fd);
  if (!written) {
    errno = code;
    fail(JailStep::OomScore);
  }
}

// fcntl only: a descriptor that is not open is skipped. nothing is closed, as that would also close the report pipe,
// and a failed exec would look like a success
void JailExec::markCloseOnExec() const noexcept {
  for (int fd = 3; fd < fdLimit_; ++fd) {
    const int flags = ::fcntl(fd, F_GETFD);
    if (flags == -1 || (flags & FD_CLOEXEC) != 0)
      continue;
    if (::fcntl(fd, F_SETFD, flags | FD_CLOEXEC) != 0)
      fail(JailStep::Cloexec);
  }
}

void JailExec::clearNonblocking() const noexcept {
  for (int fd = 0; fd < 3; ++fd) {
    const int flags = ::fcntl(fd, F_GETFL);
    if (flags == -1 || ((flags & O_NONBLOCK) != 0 && ::fcntl(fd, F_SETFL, flags & ~O_NONBLOCK) != 0))
      fail(JailStep::Nonblock);
  }
}

// async-signal-safe calls only: everything it reads was laid out before the fork
void JailExec::enter() const noexcept {
  // audiod blocks some signals and ignores SIGPIPE, which an exec would keep. kill and stop, and the c library's own,
  // refuse a new action, which leaves them as they are
  for (int signo = 1; signo < NSIG; ++signo)
    ::sigaction(signo, &default_, nullptr);
  if (::sigprocmask(SIG_SETMASK, &unblocked_, nullptr) != 0)
    fail(JailStep::Signals);
  if (jailed_) {
    if (::unshare(plan_.unshareFlags) != 0)
      fail(JailStep::Unshare);
    for (std::size_t i = 0; i < mounts_.size(); ++i) {
      const auto& mount = mounts_[i];
      if (::mount(mount.source, mount.target, mount.fstype, mount.flags, nullptr) != 0)
        fail(JailStep::Mount, static_cast<int>(i));
    }
    if (::chroot(plan_.root.c_str()) != 0)
      fail(JailStep::Chroot);
  }
  if (::chdir("/") != 0)
    fail(JailStep::Chdir);
  // while still root: a score below audiod's own takes root to set
  lowerPriority();
  // the ids go last: a process without root can mount nothing, and a credential change clears the death signal
  if (jailed_) {
    if (::setgroups(0, nullptr) != 0)
      fail(JailStep::Setgroups);
    if (::setgid(plan_.gid) != 0)
      fail(JailStep::Setgid);
    if (::setuid(plan_.uid) != 0)
      fail(JailStep::Setuid);
  }
  // prctl is variadic: every argument as wide as the unsigned long it reads
  if (::prctl(PR_SET_NO_NEW_PRIVS, 1UL, 0UL, 0UL, 0UL) != 0)
    fail(JailStep::NoNewPrivs);
  if (::prctl(PR_SET_PDEATHSIG, static_cast<unsigned long>(SIGKILL), 0UL, 0UL, 0UL) != 0)
    fail(JailStep::DeathSignal);
  if (!stillChildOf(parent_)) {
    errno = ESRCH;
    fail(JailStep::Parent);
  }
  if (::dup2(fds_.in, STDIN_FILENO) < 0 || ::dup2(fds_.out, STDOUT_FILENO) < 0 || ::dup2(fds_.err, STDERR_FILENO) < 0)
    fail(JailStep::Stdio);
  // audiod's own descriptors, the ipc socket among them, must never reach soloist
  markCloseOnExec();
  clearNonblocking();
  ::execve(program_, argv_.data(), envp_.data());
  fail(JailStep::Exec);
}

}  // namespace cadence::audiod

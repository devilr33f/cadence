#include "audiod/jailed-process.h"

#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <sys/wait.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <condition_variable>
#include <future>
#include <mutex>
#include <system_error>
#include <utility>

namespace cadence::audiod {

struct JailedProcess::State {
  std::mutex mutex;
  std::condition_variable changed;
  // under mutex: the program's thread reaps it holding the mutex, so a signal sent under it never reaches a reused pid
  bool reaped = false;
  JailedExit exit{};
  // its own mutex, so a callback in progress never holds up a kill or a stop
  std::mutex callbackMutex;
  ExitCallback onExit;
};

namespace {

using Clock = std::chrono::steady_clock;

JailError spawnError(std::string_view step, int code, std::string_view mount = {}) {
  return {.refused = false, .reason = step, .mount = mount, .code = code};
}

// at or above 3, so the child's dup2 onto its standard streams never closes one of them
UniqueFd aboveStdio(int fd) {
  UniqueFd original(fd);
  if (!original || fd > STDERR_FILENO)
    return original;
  return UniqueFd(::fcntl(fd, F_DUPFD_CLOEXEC, STDERR_FILENO + 1));
}

bool makePipe(UniqueFd& readEnd, UniqueFd& writeEnd) {
  std::array<int, 2> fds{};
  if (::pipe2(fds.data(), O_CLOEXEC) != 0)
    return false;
  readEnd = aboveStdio(fds[0]);
  writeEnd = aboveStdio(fds[1]);
  return readEnd && writeEnd;
}

JailedExit exitOf(int status) {
  if (WIFEXITED(status))
    return {.code = WEXITSTATUS(status), .signal = 0};
  if (WIFSIGNALED(status))
    return {.code = -1, .signal = WTERMSIG(status)};
  return {};
}

// the report of the child's failing step; nothing at all when the exec closed the pipe. a torn one reads as step 0
std::optional<JailReport> readReport(int fd) {
  JailReport report{};
  std::size_t got = 0;
  auto* bytes = reinterpret_cast<char*>(&report);
  while (got < sizeof report) {
    const auto size = ::read(fd, bytes + got, sizeof report - got);
    if (size < 0 && errno == EINTR)
      continue;
    if (size <= 0)
      break;
    got += static_cast<std::size_t>(size);
  }
  if (got == 0)
    return std::nullopt;
  if (got < sizeof report)
    return JailReport{};
  return report;
}

void logError(const Log& log, const JailError& error, bool plain) {
  if (plain)
    log.error("process_spawn_failed", {{"step", error.reason}, {"errno", error.code}});
  else if (error.refused)
    log.error("jail_refused", {{"reason", error.reason}, {"mount", error.mount}});
  else if (!error.mount.empty())
    log.error("jail_spawn_failed", {{"step", error.reason}, {"mount", error.mount}, {"errno", error.code}});
  else
    log.error("jail_spawn_failed", {{"step", error.reason}, {"errno", error.code}});
}

}  // namespace

Result<std::unique_ptr<JailedProcess>, JailError> JailedProcess::start(const Log& log, const JailPlan& plan,
                                                                       std::vector<std::string> args,
                                                                       ExitCallback onExit) {
  return spawn(log, plan, std::move(args), std::move(onExit), Streams::Separate, Confinement::Soloist);
}

Result<std::unique_ptr<JailedProcess>, JailError> JailedProcess::startPlain(const Log& log, const PlainPlan& plan,
                                                                            std::vector<std::string> args,
                                                                            ExitCallback onExit) {
  // the jail's program and build are the same host path when nothing is jailed
  const JailPlan unjailed{.root = {},
                          .unshareFlags = 0,
                          .mounts = {},
                          .program = plan.program,
                          .build = plan.program,
                          .env = plan.env,
                          .nice = plan.nice,
                          .oomScoreAdj = plan.oomScoreAdj,
                          .uid = 0,
                          .gid = 0};
  return spawn(log, unjailed, std::move(args), std::move(onExit), plan.output ? Streams::Separate : Streams::None,
               Confinement::None);
}

Result<std::unique_ptr<JailedProcess>, JailError> JailedProcess::spawn(const Log& log, const JailPlan& plan,
                                                                       std::vector<std::string> args,
                                                                       ExitCallback onExit, Streams streams,
                                                                       Confinement confinement) {
  const bool plain = confinement == Confinement::None;
  // soloist's jail as this build makes it: none in a jail-off build
  const bool jailed = !plain && kSoloistJailed;
  const auto fail = [&](const JailError& error) {
    logError(log, error, plain);
    return error;
  };
  if (jailed) {
    if (const auto refusal = checkJail(plan))
      return fail({.refused = true, .reason = refusal->reason, .mount = refusal->mount, .code = 0});
  }
  UniqueFd outRead;
  UniqueFd outWrite;
  UniqueFd errRead;
  UniqueFd errWrite;
  UniqueFd reportRead;
  UniqueFd reportWrite;
  const bool output = streams != Streams::None;
  const bool errors = streams == Streams::Separate;
  const auto readable = [](UniqueFd& read, UniqueFd& write) {
    return makePipe(read, write) && ::fcntl(read.get(), F_SETFL, O_NONBLOCK) == 0;
  };
  if ((output && !readable(outRead, outWrite)) || (errors && !readable(errRead, errWrite)) ||
      !makePipe(reportRead, reportWrite))
    return fail(spawnError("pipe", errno));
  UniqueFd null = aboveStdio(::open("/dev/null", O_RDWR | O_CLOEXEC));
  if (!null)
    return fail(spawnError("open", errno));
  auto exec = std::make_unique<JailExec>(plan, args,
                                         JailFds{.in = null.get(),
                                                 .out = output ? outWrite.get() : null.get(),
                                                 .err = errors ? errWrite.get() : null.get(),
                                                 .report = reportWrite.get()},
                                         jailed);
  std::vector<std::string_view> mounts;
  for (const auto& mount : plan.mounts)
    mounts.push_back(mount.name);

  auto state = std::make_shared<State>();
  state->onExit = std::move(onExit);
  std::promise<Result<pid_t, JailError>> spawned;
  auto result = spawned.get_future();
  std::thread thread;
  try {
    thread = std::thread([state, exec = std::move(exec), mounts = std::move(mounts), null = std::move(null),
                          outWrite = std::move(outWrite), errWrite = std::move(errWrite),
                          reportRead = std::move(reportRead), reportWrite = std::move(reportWrite),
                          spawned = std::move(spawned)]() mutable {
      const pid_t pid = ::fork();
      if (pid == 0)
        exec->enter();
      const int forkError = errno;
      // the child holds its own copies; the report pipe ends once no write end is left but the child's
      null.reset();
      outWrite.reset();
      errWrite.reset();
      reportWrite.reset();
      if (pid < 0) {
        spawned.set_value(spawnError("fork", forkError));
        return;
      }
      if (const auto report = readReport(reportRead.get())) {
        while (::waitpid(pid, nullptr, 0) < 0 && errno == EINTR) {
        }
        const bool named = report->mount >= 0 && static_cast<std::size_t>(report->mount) < mounts.size();
        spawned.set_value(spawnError(jailStepName(report->step), report->code,
                                     named ? mounts[static_cast<std::size_t>(report->mount)] : std::string_view()));
        return;
      }
      spawned.set_value(pid);

      // waits without reaping, then reaps under the mutex, so kill and stop never signal a pid already given away
      siginfo_t info{};
      while (::waitid(P_PID, static_cast<id_t>(pid), &info, WEXITED | WNOWAIT) != 0 && errno == EINTR) {
      }
      {
        std::scoped_lock lock(state->mutex);
        int status = 0;
        pid_t reaped = -1;
        while ((reaped = ::waitpid(pid, &status, 0)) < 0 && errno == EINTR) {
        }
        state->exit = reaped == pid ? exitOf(status) : JailedExit{};
        state->reaped = true;
      }
      state->changed.notify_all();
      std::scoped_lock lock(state->callbackMutex);
      if (state->onExit)
        state->onExit(state->exit);
    });
  } catch (const std::system_error& error) {
    return fail(spawnError("thread", error.code().value()));
  }
  auto pid = result.get();
  if (!pid.ok()) {
    thread.join();
    return fail(pid.error());
  }
  return std::unique_ptr<JailedProcess>(
      new JailedProcess(state, std::move(thread), std::move(outRead), std::move(errRead), pid.value()));
}

JailedProcess::JailedProcess(std::shared_ptr<State> state, std::thread thread, UniqueFd output, UniqueFd errors,
                             pid_t pid)
    : state_(std::move(state)),
      thread_(std::move(thread)),
      output_(std::move(output)),
      errors_(std::move(errors)),
      pid_(pid) {}

JailedProcess::~JailedProcess() {
  {
    std::scoped_lock lock(state_->callbackMutex);
    state_->onExit = nullptr;
  }
  bool reaped = false;
  {
    std::unique_lock lock(state_->mutex);
    if (!state_->reaped) {
      ::kill(pid_, SIGKILL);
      if (!reapTimedOut_)
        state_->changed.wait_for(lock, kReapWait, [&] { return state_->reaped; });
    }
    reaped = state_->reaped;
  }
  // a program stuck in an uninterruptible sleep dies only once it wakes: its thread, holding the state, reaps it then
  if (reaped)
    thread_.join();
  else
    thread_.detach();
}

UniqueFd JailedProcess::takeOutput() { return std::move(output_); }

UniqueFd JailedProcess::takeErrors() { return std::move(errors_); }

bool JailedProcess::reaped() const {
  std::scoped_lock lock(state_->mutex);
  return state_->reaped;
}

void JailedProcess::kill(int signo) {
  std::scoped_lock lock(state_->mutex);
  if (!state_->reaped)
    ::kill(pid_, signo);
}

std::optional<JailedExit> JailedProcess::stop(std::chrono::milliseconds grace) {
  std::unique_lock lock(state_->mutex);
  const auto reaped = [&] { return state_->reaped; };
  if (!state_->reaped) {
    ::kill(pid_, SIGTERM);
    if (!state_->changed.wait_for(lock, grace, reaped)) {
      ::kill(pid_, SIGKILL);
      reapTimedOut_ = !state_->changed.wait_for(lock, kReapWait, reaped);
    }
  }
  if (!state_->reaped)
    return std::nullopt;
  return state_->exit;
}

std::optional<JailedExit> JailedProcess::waitUntil(Clock::time_point deadline) {
  std::unique_lock lock(state_->mutex);
  if (!state_->changed.wait_until(lock, deadline, [&] { return state_->reaped; }))
    return std::nullopt;
  return state_->exit;
}

Result<Captured, JailError> JailedProcess::runCapture(const Log& log, const JailPlan& plan,
                                                      std::vector<std::string> args, std::chrono::milliseconds limit,
                                                      std::size_t maxBytes) {
  const auto deadline = Clock::now() + limit;
  auto started = spawn(log, plan, std::move(args), nullptr, Streams::StdoutOnly, Confinement::Soloist);
  return JailedProcess::captured(std::move(started), deadline, maxBytes);
}

Result<Captured, JailError> JailedProcess::runCapturePlain(const Log& log, const PlainPlan& plan,
                                                           std::vector<std::string> args,
                                                           std::chrono::milliseconds limit, std::size_t maxBytes) {
  const auto deadline = Clock::now() + limit;
  // the jail's program and build are the same host path when nothing is jailed
  const JailPlan unjailed{.root = {},
                          .unshareFlags = 0,
                          .mounts = {},
                          .program = plan.program,
                          .build = plan.program,
                          .env = plan.env,
                          .nice = plan.nice,
                          .oomScoreAdj = plan.oomScoreAdj,
                          .uid = 0,
                          .gid = 0};
  auto started = spawn(log, unjailed, std::move(args), nullptr, Streams::StdoutOnly, Confinement::None);
  return JailedProcess::captured(std::move(started), deadline, maxBytes);
}

// the capture both runCapture forms share: its stdout to the end or the limit, then its exit
Result<Captured, JailError> JailedProcess::captured(Result<std::unique_ptr<JailedProcess>, JailError> started,
                                                    std::chrono::steady_clock::time_point deadline,
                                                    std::size_t maxBytes) {
  if (!started.ok())
    return started.error();
  if (!started.ok())
    return started.error();
  auto& process = *started.value();
  const auto output = process.takeOutput();
  Captured captured;
  bool closed = false;
  std::array<char, 1024> chunk{};
  while (!closed && !captured.truncated) {
    // rounded up, so the limit never passes early
    const auto left = std::chrono::ceil<std::chrono::milliseconds>(deadline - Clock::now()).count();
    if (left <= 0) {
      captured.timedOut = true;
      break;
    }
    pollfd ready{.fd = output.get(), .events = POLLIN, .revents = 0};
    const int polled = ::poll(&ready, 1, static_cast<int>(left));
    if (polled == 0 || (polled < 0 && errno == EINTR))
      continue;
    const auto size = polled < 0 ? -1 : ::read(output.get(), chunk.data(), chunk.size());
    if (size < 0 && (errno == EAGAIN || errno == EINTR))
      continue;
    if (size <= 0) {
      closed = true;
      break;
    }
    captured.output.append(chunk.data(), static_cast<std::size_t>(size));
    if (captured.output.size() > maxBytes) {
      captured.output.resize(maxBytes);
      captured.truncated = true;
    }
  }
  if (closed) {
    captured.exit = process.waitUntil(deadline);
    // its stdout closed, but it kept running
    captured.timedOut = !captured.exit;
  }
  if (!captured.exit) {
    process.kill(SIGKILL);
    captured.exit = process.waitUntil(Clock::now() + kReapWait);
    process.reapTimedOut_ = !captured.exit;
  }
  return captured;
}

}  // namespace cadence::audiod

#include "audiod/airplay-supervisor.h"

#include "audiod/sys.h"
#include "dbus/bus.h"

#include <poll.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <cerrno>
#include <chrono>
#include <deque>
#include <mutex>
#include <optional>
#include <thread>
#include <utility>
#include <variant>
#include <vector>

namespace cadence::audiod {

namespace {

using Clock = std::chrono::steady_clock;

// a poll wakes at least this often, so a deadline far off never overflows its timeout
constexpr std::chrono::milliseconds kLongestWait{60'000};
// how often daemons a stop could not reap are looked at again, before the next attempt may start (v1's RETRY_POLL)
constexpr std::chrono::milliseconds kReapPoll{50};

std::uint64_t seconds(Clock::duration duration) {
  return static_cast<std::uint64_t>(
      std::max<std::int64_t>(0, std::chrono::duration_cast<std::chrono::seconds>(duration).count()));
}

void wake(int fd) {
  const std::uint64_t one = 1;
  [[maybe_unused]] const auto written = ::write(fd, &one, sizeof one);
}

}  // namespace

class AirplaySupervisor::Worker {
 public:
  Worker(const Log& log, AirplayHooks hooks, AirplayOptions options)
      : log_(log),
        output_(log),
        hooks_(std::move(hooks)),
        options_(std::move(options)),
        commandFd_(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK)),
        stopFd_(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK)),
        backoff_(options_.timings.firstBackoff) {}

  ~Worker() { stop(); }

  Worker(const Worker&) = delete;
  Worker& operator=(const Worker&) = delete;

  void receiver(std::shared_ptr<AirplayReceiver> receiver) { receiver_ = std::move(receiver); }

  void start() {
    if (!thread_.joinable())
      thread_ = std::thread([this] { loop(); });
  }

  void stop() {
    if (!thread_.joinable())
      return;
    wake(stopFd_.get());
    thread_.join();
  }

  struct Configure {
    AirplaySettings settings;
  };
  struct Gate {
    bool open = false;
  };
  struct Task {
    std::function<void()> run;
  };
  struct Metadata {
    MediaMetadata metadata;
  };
  struct Progress {
    airplay::Progress progress;
  };
  using Command = std::variant<Configure, Gate, Task, Metadata, Progress>;

  // any thread
  void post(Command command) {
    {
      std::scoped_lock lock(mutex_);
      queue_.push_back(std::move(command));
    }
    wake(commandFd_.get());
  }

  [[nodiscard]] AirplayOutput& output() { return output_; }
  [[nodiscard]] const AirplayOutput& output() const { return output_; }

 private:
  void loop() {
    report();
    for (;;) {
      std::array<pollfd, 3> fds{};
      std::size_t count = 0;
      fds[count++] = {.fd = stopFd_.get(), .events = POLLIN, .revents = 0};
      fds[count++] = {.fd = commandFd_.get(), .events = POLLIN, .revents = 0};
      const bool bus = running_ && stack_->client().open();
      if (bus)
        fds[count++] = {.fd = stack_->client().fd(), .events = POLLIN, .revents = 0};
      const int timeout = bus && stack_->client().hasBuffered() ? 0 : waitMs(Clock::now());
      if (::poll(fds.data(), count, timeout) < 0 && errno != EINTR) {
        log_.error("airplay_poll_failed", {{"errno", errno}});
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
      }
      if ((fds[0].revents & POLLIN) != 0)
        break;
      if ((fds[1].revents & POLLIN) != 0)
        commands();
      // a command may have stopped the stack the bus was of
      if (bus && running_)
        stack_->client().process();
      if (running_ && receiver_)
        receiver_->tick(Clock::now());
      health(Clock::now());
      reconcile(Clock::now());
      report();
    }
    stopStack();
  }

  [[nodiscard]] int waitMs(Clock::time_point now) const {
    // a config an attempt's wait took in is acted on at once, and so is a receiver that failed as it attached
    if (dirty_ || (running_ && receiver_ && !receiver_->failure().empty()))
      return 0;
    std::optional<Clock::time_point> next = restartAt_;
    const auto consider = [&](std::optional<Clock::time_point> at) {
      if (at && (!next || *at < *next))
        next = at;
    };
    if (running_)
      consider(stack_->client().nextDeadline());
    if (running_ && receiver_)
      consider(receiver_->deadline());
    if (!dying_.empty())
      consider(now + kReapPoll);
    if (!next)
      return static_cast<int>(kLongestWait.count());
    const auto left = std::chrono::ceil<std::chrono::milliseconds>(*next - now);
    return static_cast<int>(std::clamp(left, std::chrono::milliseconds::zero(), kLongestWait).count());
  }

  // only state changes: what they mean is acted on by reconcile, or ends an attempt in progress
  void commands() {
    std::uint64_t ignored = 0;
    [[maybe_unused]] const auto read = ::read(commandFd_.get(), &ignored, sizeof ignored);
    std::deque<Command> taken;
    {
      std::scoped_lock lock(mutex_);
      taken.swap(queue_);
    }
    for (auto& command : taken)
      std::visit([&](auto& body) { handle(body); }, command);
  }

  void handle(Configure& command) {
    if (settings_ && *settings_ == command.settings)
      return;
    settings_ = std::move(command.settings);
    log_.info("airplay_configured", {{"enabled", settings_->enabled ? 1 : 0}});
    dirty_ = true;
    backoff_ = options_.timings.firstBackoff;
    restartAt_.reset();
  }

  void handle(const Gate& command) { gateOpen_ = command.open; }

  void handle(Task& command) {
    if (command.run)
      command.run();
  }

  // what the pipe read before the receiver attached, or after it went, is of no session
  void handle(const Metadata& command) {
    if (running_ && receiver_)
      receiver_->metadata(command.metadata);
  }

  void handle(const Progress& command) {
    if (running_ && receiver_)
      receiver_->progress(command.progress);
  }

  [[nodiscard]] bool wanted() const { return settings_ && settings_->enabled; }

  // an attempt's wait: the stop and the commands are watched meanwhile
  bool idle(std::chrono::milliseconds wait) {
    std::array<pollfd, 2> fds{{{.fd = stopFd_.get(), .events = POLLIN, .revents = 0},
                               {.fd = commandFd_.get(), .events = POLLIN, .revents = 0}}};
    if (::poll(fds.data(), fds.size(), static_cast<int>(wait.count())) < 0 && errno != EINTR)
      return false;
    // the stop is left to be read by the loop, which ends on it
    if ((fds[0].revents & POLLIN) != 0)
      return false;
    if ((fds[1].revents & POLLIN) != 0)
      commands();
    return wanted() && !dirty_ && gateOpen_;
  }

  void reconcile(Clock::time_point now) {
    if (dirty_) {
      dirty_ = false;
      stopStack();
      setState(wanted() ? AirplayState::Starting : AirplayState::Off);
    }
    // every daemon of the last attempt is reaped before the next one starts
    std::erase_if(dying_, [](const std::unique_ptr<JailedProcess>& process) { return process->reaped(); });
    if (!dying_.empty())
      return;
    if (restartAt_) {
      if (now < *restartAt_)
        return;
      // due: a past deadline left armed would wake the poll at once, over and over
      restartAt_.reset();
    }
    // the gate opens once a volume held: until then the receiver waits, and starts as it opens
    if (!wanted() || stack_ || !gateOpen_)
      return;
    attempt();
  }

  void attempt() {
    setState(AirplayState::Starting);
    report();
    const auto began = Clock::now();
    const auto serial = ++attempts_;
    log_.info("airplay_start", {{"attempt", serial}});
    stack_ = std::make_unique<AirplayStack>(
        log_, *settings_, options_, output_,
        AirplayStack::Hooks{.idle = [this](std::chrono::milliseconds wait) { return idle(wait); },
                            .exited = [this] { wake(commandFd_.get()); },
                            .stopFd = stopFd_.get(),
                            .metadata = [this](const MediaMetadata& metadata) { post(Metadata{metadata}); },
                            .progress = [this](const airplay::Progress& progress) { post(Progress{progress}); }});
    const auto code = stack_->start();
    if (code == airplay::kInterrupted) {
      log_.info("airplay_start_interrupted", {{"attempt", serial}});
      stopStack();
      return;
    }
    if (!code.empty()) {
      log_.warn("airplay_failed", {{"attempt", serial}, {"code", code}});
      stopStack();
      setState(AirplayState::Failed, code);
      restartLater(Clock::now() - began);
      return;
    }
    auto& client = stack_->client();
    client.setSignalHandler([this](const dbus::Message& message) { signal(message); });
    // a change of shairport's owner the start read before the handler was in went unseen: the name is asked once more
    if (!stack_->stillOwner() || ownerLost_) {
      log_.warn("airplay_failed", {{"attempt", serial}, {"code", kAirplaySessionLost}});
      stopStack();
      setState(AirplayState::Failed, kAirplaySessionLost);
      restartLater(Clock::now() - began);
      return;
    }
    running_ = true;
    startedAt_ = began;
    log_.info("airplay_ready", {{"attempt", serial}});
    setState(AirplayState::Ready);
    if (receiver_)
      receiver_->attached(client, stack_->owner());
  }

  // shairport's name taken by anyone else, or let go, ends the receiver; the rest is the receiver's
  void signal(const dbus::Message& message) {
    if (const auto changed = dbus::parseNameOwnerChanged(message)) {
      if (changed->name == airplay::kShairportName && changed->newOwner != stack_->owner())
        ownerLost_ = true;
      return;
    }
    if (receiver_)
      receiver_->signal(message);
  }

  void health(Clock::time_point now) {
    if (!running_)
      return;
    std::string_view reason;
    // the receiver's own failure is its code; it goes with the receiver's detach, so it is copied first
    std::string code(kAirplaySessionLost);
    if (stack_->exited()) {
      reason = "exited";
    } else if (!stack_->client().open()) {
      reason = "bus";
    } else if (ownerLost_) {
      reason = "owner";
    } else if (receiver_ && !receiver_->failure().empty()) {
      reason = "receiver";
      code = receiver_->failure();
    }
    if (reason.empty())
      return;
    const auto ran = now - startedAt_;
    log_.warn("airplay_session_lost", {{"reason", reason}, {"code", code}, {"ran_s", seconds(ran)}});
    stopStack();
    setState(AirplayState::Failed, code);
    restartLater(ran);
  }

  void stopStack() {
    if (!stack_)
      return;
    if (running_ && receiver_)
      receiver_->detached();
    running_ = false;
    ownerLost_ = false;
    for (auto& process : stack_->stop())
      dying_.push_back(std::move(process));
    stack_.reset();
  }

  void restartLater(Clock::duration ran) {
    const auto& timings = options_.timings;
    if (ran >= timings.steady)
      backoff_ = timings.firstBackoff;
    restartAt_ = Clock::now() + backoff_;
    backoff_ = std::min(backoff_ * 2, timings.lastBackoff);
  }

  void setState(AirplayState state, std::string_view code = {}) {
    status_.state = state;
    status_.code = std::string(code);
  }

  void report() {
    if (reported_ && *reported_ == status_)
      return;
    reported_ = status_;
    if (hooks_.status)
      hooks_.status(status_);
  }

  const Log& log_;
  AirplayOutput output_;
  AirplayHooks hooks_;
  const AirplayOptions options_;
  UniqueFd commandFd_;
  UniqueFd stopFd_;

  std::mutex mutex_;
  std::deque<Command> queue_;

  // set before the thread starts
  std::shared_ptr<AirplayReceiver> receiver_;

  // the supervisor thread's own
  std::optional<AirplaySettings> settings_;
  // a new config: what runs goes, and the next attempt starts afresh
  bool dirty_ = false;
  bool gateOpen_ = false;
  std::uint64_t attempts_ = 0;
  std::chrono::milliseconds backoff_;
  std::optional<Clock::time_point> restartAt_;
  // shairport owns its name: the attempt that made the stack succeeded
  bool running_ = false;
  bool ownerLost_ = false;
  Clock::time_point startedAt_{};
  AirplayStatus status_;
  std::optional<AirplayStatus> reported_;
  // after the fds and the output its daemons report to and push into
  std::unique_ptr<AirplayStack> stack_;
  std::vector<std::unique_ptr<JailedProcess>> dying_;

  std::thread thread_;
};

AirplaySupervisor::AirplaySupervisor(const Log& log, AirplayHooks hooks, AirplayOptions options)
    : worker_(std::make_unique<Worker>(log, std::move(hooks), std::move(options))) {}

AirplaySupervisor::~AirplaySupervisor() { stop(); }

void AirplaySupervisor::receiver(std::shared_ptr<AirplayReceiver> receiver) {
  worker_->receiver(std::move(receiver));
}

void AirplaySupervisor::start() { worker_->start(); }

void AirplaySupervisor::stop() { worker_->stop(); }

void AirplaySupervisor::configure(AirplaySettings settings) {
  worker_->post(Worker::Configure{std::move(settings)});
}

void AirplaySupervisor::gate(bool open) {
  worker_->output().gate(open);
  worker_->post(Worker::Gate{open});
}

void AirplaySupervisor::post(std::function<void()> task) { worker_->post(Worker::Task{std::move(task)}); }

void AirplaySupervisor::openOutput(PcmInput& music) { worker_->output().open(music); }

void AirplaySupervisor::closeOutput() { worker_->output().close(); }

void AirplaySupervisor::holdOutput(bool held) { worker_->output().hold(held); }

std::uint64_t AirplaySupervisor::pauseOutput() { return worker_->output().pause(); }

void AirplaySupervisor::releaseOutput(std::uint64_t pausesTaken) { worker_->output().release(pausesTaken); }

std::uint64_t AirplaySupervisor::outputPauses() const { return worker_->output().pauses(); }

}  // namespace cadence::audiod

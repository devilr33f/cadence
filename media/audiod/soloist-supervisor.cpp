#include "audiod/soloist-supervisor.h"

#include "audiod/jailed-process.h"
#include "audiod/sender-volume.h"
#include "audiod/soloist-jail.h"
#include "audiod/soloist-output.h"
#include "audiod/soloist-resolv.h"
#include "audiod/soloist-scratch.h"
#include "audiod/sys.h"

#include <poll.h>
#include <sys/eventfd.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <atomic>
#include <cerrno>
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

// soloist's own exit code for a build past its date
constexpr int kExpired = 10;
// the jail's data dir; a jail-off build has nothing bound there and gives the scratch's own home instead
constexpr std::string_view kJailHome = "/home/soloist";
// a poll wakes at least this often, so a deadline far off never overflows its timeout
constexpr std::chrono::milliseconds kLongestWait{60'000};
// what one wake reads from each of soloist's pipes
constexpr std::size_t kOutputBudget = 64 * 1024;

bool regularFile(const std::string& path) {
  struct stat info {};
  return ::stat(path.c_str(), &info) == 0 && S_ISREG(info.st_mode);
}

std::uint64_t seconds(Clock::duration duration) {
  return static_cast<std::uint64_t>(std::max<std::int64_t>(
      0, std::chrono::duration_cast<std::chrono::seconds>(duration).count()));
}

std::string_view probeToken(const SoloistProbe& probe) { return probe.code.empty() ? "ok" : probe.code; }

// what a build file is, so its version is probed again only when it changed
// fixed widths: 32-bit android's dev_t, ino_t and off_t are narrower than its struct stat's fields
struct FileId {
  std::uint64_t device = 0;
  std::uint64_t inode = 0;
  std::int64_t size = 0;
  std::int64_t modified = 0;
  bool operator==(const FileId&) const = default;
};

std::optional<FileId> fileId(const std::string& path) {
  struct stat info {};
  if (::stat(path.c_str(), &info) != 0)
    return std::nullopt;
  return FileId{.device = static_cast<std::uint64_t>(info.st_dev),
                .inode = static_cast<std::uint64_t>(info.st_ino),
                .size = static_cast<std::int64_t>(info.st_size),
                .modified = static_cast<std::int64_t>(info.st_mtim.tv_sec) * 1'000'000'000 + info.st_mtim.tv_nsec};
}

// the plan a build makes of the resolved paths and its own --version live in soloist-launch (SoloistPlan,
// soloistPlan, probePlan): a jailed build runs the jail's, an unjailed one the plain plan

}  // namespace

class SoloistSupervisor::Worker {
 public:
  Worker(const Log& log, SoloistHooks hooks, SoloistOptions options)
      : log_(log),
        hooks_(std::move(hooks)),
        options_(std::move(options)),
        commandFd_(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK)),
        stopFd_(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK)),
        backoff_(options_.timings.firstBackoff),
        prober_(log, hooks_.probed, options_.timings.probeLimit) {}

  ~Worker() { stop(); }

  Worker(const Worker&) = delete;
  Worker& operator=(const Worker&) = delete;

  void start() {
    if (thread_.joinable())
      return;
    prober_.start();
    thread_ = std::thread([this] { loop(); });
  }

  void stop() {
    if (thread_.joinable()) {
      wake(stopFd_.get());
      thread_.join();
    }
    // after the thread that hands it probes
    prober_.stop();
  }

  struct Configure {
    SoloistSettings settings;
  };
  struct Gate {
    bool open = false;
  };
  struct Probe {
    std::string requestId;
    std::string path;
  };
  struct Switch {};
  struct Exited {
    std::uint64_t run = 0;
    JailedExit exit;
  };
  struct Control {
    soloist::Command command = soloist::Command::Pause;
    ControlAnswer answer;
  };
  struct Level {
    std::uint8_t level = 0;
  };
  using Command = std::variant<Configure, Gate, Probe, Switch, Exited, Control, Level>;

  // before start
  void events(std::shared_ptr<MediaSourceEvents> events) { events_ = std::move(events); }

  // any thread
  void openOutput(PcmInput& music) {
    std::scoped_lock lock(pcmMutex_);
    output_ = &music;
    if (pcm_)
      pcm_->openOutput(music);
  }
  void closeOutput() {
    std::scoped_lock lock(pcmMutex_);
    output_ = nullptr;
    if (pcm_)
      pcm_->closeOutput();
  }

  // any thread
  void post(Command command) {
    {
      std::scoped_lock lock(mutex_);
      queue_.push_back(std::move(command));
    }
    wake(commandFd_.get());
  }

 private:
  // the latest control cadenced waits on: soloist answers each command in order, so only the latest can be told
  struct Pending {
    soloist::Command command = soloist::Command::Pause;
    ControlAnswer answer;
    Clock::time_point deadline;
  };

  struct Run {
    std::uint64_t serial = 0;
    Clock::time_point started;
    std::unique_ptr<JailedProcess> process;
    // its stdout and its stderr, each scanned for login failed and made into its lines
    UniqueFd output;
    UniqueFd errors;
    OutputScanner scanner;
    OutputScanner errorScanner;
    std::optional<ChildLog> lines;
    std::optional<SoloistBuild> build;
    Clock::time_point tickAt;
    // dropped after a failed connect or a close, and read again: it may be stale
    std::optional<std::uint16_t> port;
    // the wait for the first port is over
    bool portSeen = false;
    std::unique_ptr<soloist::Connection> connection;
    // a new one with each connection, as soloist starts its own state over on each
    std::optional<SoloistSession> session;
    // a websocket handshake completed, whatever soloist sent first
    bool answered = false;
    // the socket closed while soloist ran: told at the reconnect, once it is clear soloist did not exit
    bool dropped = false;
    bool authFailed = false;
    bool playing = false;
  };

  static void wake(int fd) {
    const std::uint64_t one = 1;
    [[maybe_unused]] const auto written = ::write(fd, &one, sizeof one);
  }

  void loop() {
    report();
    for (;;) {
      std::array<pollfd, 5> fds{};
      std::size_t count = 0;
      fds[count++] = {.fd = stopFd_.get(), .events = POLLIN, .revents = 0};
      fds[count++] = {.fd = commandFd_.get(), .events = POLLIN, .revents = 0};
      const std::uint64_t polled = run_ ? run_->serial : 0;
      std::optional<std::size_t> output;
      std::optional<std::size_t> errors;
      std::optional<std::size_t> socket;
      if (run_ && run_->output) {
        output = count;
        fds[count++] = {.fd = run_->output.get(), .events = POLLIN, .revents = 0};
      }
      if (run_ && run_->errors) {
        errors = count;
        fds[count++] = {.fd = run_->errors.get(), .events = POLLIN, .revents = 0};
      }
      if (run_ && run_->connection && run_->connection->fd() >= 0) {
        socket = count;
        fds[count++] = {.fd = run_->connection->fd(), .events = POLLIN, .revents = 0};
      }
      const int ready = ::poll(fds.data(), count, waitMs(Clock::now()));
      if (ready < 0 && errno != EINTR) {
        log_.error("soloist_poll_failed", {{"errno", errno}});
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
      }
      if ((fds[0].revents & POLLIN) != 0)
        break;
      if ((fds[1].revents & POLLIN) != 0) {
        commands();
        dropped(Clock::now());
      }
      // a command may have ended the run those descriptors were of
      const auto same = [&] { return run_ && run_->serial == polled; };
      if (output && fds[*output].revents != 0 && same())
        readOutput(ChildLog::Stream::Out);
      if (errors && fds[*errors].revents != 0 && same())
        readOutput(ChildLog::Stream::Err);
      if (socket && fds[*socket].revents != 0 && same())
        drain(Clock::now());
      timers(Clock::now());
      reconcile(Clock::now());
      report();
    }
    if (run_)
      finish();
    std::scoped_lock lock(pcmMutex_);
    pcm_.reset();
    resolv_.reset();
  }

  [[nodiscard]] int waitMs(Clock::time_point now) const {
    std::optional<Clock::time_point> next;
    const auto consider = [&](std::optional<Clock::time_point> at) {
      if (at && (!next || *at < *next))
        next = at;
    };
    if (run_) {
      consider(run_->tickAt);
      if (run_->lines)
        consider(run_->lines->deadline());
      if (!run_->answered)
        consider(run_->started + (run_->portSeen ? options_.timings.answerWait : options_.timings.portWait));
    }
    consider(restartAt_);
    consider(switchAt_);
    consider(coalescer_.deadline());
    if (pending_)
      consider(pending_->deadline);
    if (!next)
      return static_cast<int>(kLongestWait.count());
    const auto left = std::chrono::ceil<std::chrono::milliseconds>(*next - now);
    return static_cast<int>(std::clamp(left, std::chrono::milliseconds::zero(), kLongestWait).count());
  }

  void commands() {
    std::uint64_t ignored = 0;
    [[maybe_unused]] const auto read = ::read(commandFd_.get(), &ignored, sizeof ignored);
    std::deque<Command> taken;
    {
      std::scoped_lock lock(mutex_);
      taken.swap(queue_);
    }
    // a command may block (a probe, a stop): each one gets its own now
    for (auto& command : taken)
      std::visit([&](auto& body) { handle(body, Clock::now()); }, command);
  }

  void handle(Configure& command, Clock::time_point /*now*/) {
    if (settings_ && *settings_ == command.settings)
      return;
    auto next = std::move(command.settings);
    // a new initial volume only matters at the next start
    auto runless = next;
    runless.initialVolume = settings_ ? settings_->initialVolume : runless.initialVolume;
    const bool restart = !settings_ || *settings_ != runless;
    settings_ = std::move(next);
    log_.info("soloist_configured", {{"enabled", settings_->enabled ? 1 : 0}, {"restart", restart ? 1 : 0}});
    if (!restart)
      return;
    halted_ = false;
    backoff_ = options_.timings.firstBackoff;
    restartAt_.reset();
    switchAt_.reset();
    if (run_)
      finish();
    if (!settings_->enabled)
      return setState(SoloistState::Off);
    if (const auto problem = keyProblem(settings_->apiKey)) {
      log_.warn("soloist_key_missing", {{"reason", *problem}});
      return setState(SoloistState::KeyMissing, *problem);
    }
    // an expired build stays so until a switch, whatever the config
    setState(expired_ ? SoloistState::Expired : SoloistState::Starting);
  }

  void handle(const Gate& command, Clock::time_point /*now*/) { gateOpen_ = command.open; }

  // the plan is made here; the --version runs on the prober's thread
  void handle(const Probe& command, Clock::time_point /*now*/) {
    if (settings_ && regularFile(command.path) && scratch()) {
      auto paths = resolveJail(log_, *settings_, command.path, options_.resolvConf, *scratch_);
      if (paths.ok())
        return prober_.probe(command.requestId, soloistPlan(paths.value()));
    }
    const SoloistProbe probe{.code = "io", .build = std::nullopt};
    log_.info("soloist_probe", {{"code", probeToken(probe)}});
    hooks_.probed(command.requestId, probe);
  }

  // the silent starts count from the last install, as cadenced rolls a new build back on them
  void handle(const Switch& /*command*/, Clock::time_point now) {
    expired_ = false;
    halted_ = false;
    silentStarts_ = 0;
    backoff_ = options_.timings.firstBackoff;
    restartAt_.reset();
    if (!run_)
      return;
    if (run_->playing) {
      log_.info("soloist_switch_held");
      switchAt_ = now + options_.timings.switchCheck;
      return;
    }
    switchNow();
  }

  void handle(const Exited& command, Clock::time_point now) {
    if (!run_ || run_->serial != command.run)
      return;
    const auto ran = now - run_->started;
    const bool authFailed = finish();
    const auto& exit = command.exit;
    log_.info("soloist_exit", {{"code", exit.code}, {"signal", exit.signal}, {"ran_s", seconds(ran)}});
    lastExit_ = exit.signal != 0 ? -1 : exit.code;
    if (exit.signal == 0 && exit.code == kExpired) {
      expired_ = true;
      log_.warn("soloist_expired");
      return setState(SoloistState::Expired);
    }
    restartLater(ran, now);
    // a run whose login failed stays failed until the next start
    if (authFailed)
      return setState(SoloistState::Failed, "auth_failed");
    setState(SoloistState::Starting);
  }

  void handle(Control& command, Clock::time_point now) {
    // a newer command leaves an older one no one's to answer: it went out, and nothing refused it
    settle(kMediaOk);
    if (!run_ || !run_->connection) {
      log_.info("soloist_command",
                {{"command", soloist::commandName(command.command)}, {"code", soloist::kControlUnsupported}});
      if (command.answer)
        command.answer(soloist::kControlUnsupported);
      return;
    }
    const bool loggedIn = run_->session && run_->session->loggedIn();
    const auto code = run_->connection->control(command.command, loggedIn);
    if (!code.empty()) {
      if (command.answer)
        command.answer(code);
      return;
    }
    if (command.answer)
      pending_ = Pending{command.command, std::move(command.answer), now + controlAnswer_};
  }

  void handle(const Level& command, Clock::time_point now) {
    if (!run_ || !run_->session || !run_->connection) {
      guard_.setCurrent(command.level);
      return;
    }
    if (const auto level = run_->session->speakerLevel(command.level, now))
      run_->connection->setVolume(*level);
  }

  // answers the pending control, if any
  void settle(std::string_view code) {
    if (!pending_)
      return;
    auto answer = std::move(pending_->answer);
    pending_.reset();
    answer(code);
  }

  void emit(std::function<void(MediaSourceEvents&)> report) {
    if (!events_ || !hooks_.post)
      return;
    hooks_.post([events = events_, report = std::move(report)] { report(*events); });
  }

  void readOutput(ChildLog::Stream stream) {
    const bool out = stream == ChildLog::Stream::Out;
    auto& fd = out ? run_->output : run_->errors;
    const auto now = Clock::now();
    const auto drained = drainOutput(fd.get(), out ? run_->scanner : run_->errorScanner, kOutputBudget,
                                     [&](std::span<const char> bytes) {
                                       if (run_->lines)
                                         run_->lines->feed(stream, bytes, now);
                                     });
    if (drained.loginFailed && !run_->authFailed) {
      run_->authFailed = true;
      log_.warn("soloist_auth_failed");
      setState(SoloistState::Failed, "auth_failed");
    }
    if (drained.closed) {
      if (run_->lines)
        run_->lines->finish(stream, now);
      fd.reset();
    }
  }

  // each event is stamped as it is read: the echo window must not shrink while this thread did other work
  void drain(Clock::time_point /*now*/) {
    while (run_ && run_->connection) {
      auto event = run_->connection->next();
      if (!event)
        break;
      onEvent(*event, Clock::now());
    }
    dropped(Clock::now());
  }

  // a connection that closed, in a read or a send
  void dropped(Clock::time_point now) {
    if (run_ && run_->connection && !run_->connection->open())
      closed(now);
  }

  void onEvent(const soloist::Event& event, Clock::time_point now) {
    std::visit([&](const auto& body) { on(body, now); }, event);
  }

  void on(const soloist::Auth& auth, Clock::time_point now) {
    react(run_->session->auth(auth.loggedIn, auth.active, now), now);
  }
  void on(const soloist::Device& device, Clock::time_point now) {
    react(run_->session->device(device.active, now), now);
  }
  void on(const soloist::Volume& volume, Clock::time_point now) {
    react(run_->session->volume(volume.level, now), now);
  }

  void on(const soloist::Status& status, Clock::time_point /*now*/) {
    if (status.track)
      track(*status.track);
    if (status.positionMs)
      emit([ms = *status.positionMs](MediaSourceEvents& events) { events.position(ms); });
    switch (status.state) {
      case soloist::PlayState::Playing:
        // a session takes the speaker only while it is logged in and the active device, and only on the edge: a
        // playing status still in flight when our pause of a displaced soloist lands must not take the session back,
        // nor one about the account playing elsewhere (the app moved playback to the phone when it started airplay)
        if (!run_->session->loggedIn() || !run_->session->active() || run_->playing)
          return;
        run_->playing = true;
        emit([](MediaSourceEvents& events) { events.playing(); });
        return;
      case soloist::PlayState::Paused:
      case soloist::PlayState::Idle:
        run_->playing = false;
        emit([](MediaSourceEvents& events) { events.paused(); });
        if (switchAt_)
          switchNow();
        return;
      case soloist::PlayState::Buffering:
        return;
    }
  }

  void on(const soloist::Track& item, Clock::time_point /*now*/) { track(item); }
  void on(const soloist::Position& position, Clock::time_point /*now*/) {
    emit([ms = position.ms](MediaSourceEvents& events) { events.position(ms); });
  }
  void on(const soloist::Error& error, Clock::time_point /*now*/) {
    if (error.unsupported && pending_ && pending_->command == *error.unsupported)
      settle(soloist::kControlUnsupported);
  }
  void on(const soloist::Answer& answer, Clock::time_point /*now*/) {
    if (answer.control && pending_ && pending_->command == *answer.control)
      settle(kMediaOk);
  }
  void on(const soloist::Other& /*other*/, Clock::time_point /*now*/) {}

  // memory only: never logged
  void track(const soloist::Track& item) {
    emit([metadata = item.metadata, ms = item.durationMs](MediaSourceEvents& events) {
      events.metadata(metadata);
      events.duration(ms);
    });
  }

  void react(const SessionReaction& reaction, Clock::time_point now) {
    for (const auto level : reaction.send)
      run_->connection->setVolume(level);
    if (reaction.request) {
      if (const auto level = coalescer_.push(*reaction.request, now))
        request(*level);
    }
    if (reaction.gone || reaction.loggedOut) {
      coalescer_.clearPending();
      run_->playing = false;
      emit([](MediaSourceEvents& events) { events.ended(); });
      if (switchAt_)
        switchNow();
    }
  }

  void request(std::uint8_t level) {
    log_.info("soloist_sender_volume", {{"level", level}});
    if (hooks_.senderVolume)
      hooks_.senderVolume(level);
  }

  void closed(Clock::time_point now) {
    settle(kMediaOk);
    run_->session.reset();
    // soloist starts its state over on the next connection
    emit([](MediaSourceEvents& events) {
      events.metadata({});
      events.duration(0);
    });
    run_->connection.reset();
    run_->port.reset();
    run_->dropped = true;
    run_->tickAt = now + options_.timings.reconnect;
  }

  void timers(Clock::time_point now) {
    dropped(now);
    if (run_ && run_->lines)
      run_->lines->tick(now);
    if (run_) {
      if (now >= run_->tickAt)
        tick(now);
      if (silent(now))
        endSilent(now);
      else if (!run_->connection && run_->port)
        connect(now);
    }
    if (const auto level = coalescer_.poll(now))
      request(*level);
    if (pending_ && now >= pending_->deadline)
      settle(kMediaOk);
    if (switchAt_ && now >= *switchAt_) {
      switchAt_.reset();
      if (run_ && run_->playing)
        switchAt_ = now + options_.timings.switchCheck;
      else if (run_)
        switchNow();
    }
  }

  // the port is read only after this run's start removed the last one's
  void tick(Clock::time_point now) {
    if (!run_->port && (run_->portSeen || now < run_->started + options_.timings.portWait)) {
      if (const auto port = soloist::readPort(scratch_->home)) {
        run_->port = port;
        run_->portSeen = true;
      }
    }
    run_->tickAt = now + (run_->portSeen ? options_.timings.tick : options_.timings.portTick);
  }

  [[nodiscard]] bool silent(Clock::time_point now) const {
    const auto& timings = options_.timings;
    return !run_->answered &&
           (now >= run_->started + timings.answerWait || (!run_->portSeen && now >= run_->started + timings.portWait));
  }

  void connect(Clock::time_point now) {
    if (std::exchange(run_->dropped, false))
      log_.warn("soloist_session_lost");
    soloist::ControlOptions control;
    control.client.stopFd = stopFd_.get();
    auto connected = soloist::connect(log_, *run_->port, std::move(control));
    if (!connected.ok()) {
      run_->port.reset();
      return;
    }
    run_->connection = std::move(connected.value());
    // armed at once: soloist may send the app's stored level before it says a session is up
    run_->session.emplace(guard_);
    if (!run_->answered) {
      run_->answered = true;
      silentStarts_ = 0;
      log_.info("soloist_answered");
      if (!run_->authFailed)
        setState(SoloistState::Ready);
    }
    // frames may have come behind the handshake
    drain(now);
  }

  void endSilent(Clock::time_point now) {
    const auto ran = now - run_->started;
    log_.warn("soloist_silent", {{"ran_s", seconds(ran)}});
    finish();
    ++silentStarts_;
    restartLater(ran, now);
    setState(SoloistState::Starting);
  }

  void switchNow() {
    log_.info("soloist_switch");
    switchAt_.reset();
    finish();
    backoff_ = options_.timings.firstBackoff;
    restartAt_.reset();
  }

  // the run is over: what its pipes still hold is read first (soloist's last lines, a login failed it printed as it
  // went: its exit may be taken before them), at most the budget from each and without waiting; then its connection is
  // closed and its process killed, if it still runs, and reaped. true when its login failed
  bool finish() {
    for (const auto stream : {ChildLog::Stream::Out, ChildLog::Stream::Err}) {
      if ((stream == ChildLog::Stream::Out ? run_->output : run_->errors).valid())
        readOutput(stream);
    }
    auto run = std::move(run_);
    settle(kMediaOk);
    coalescer_.clearPending();
    if (run->answered)
      emit([](MediaSourceEvents& events) { events.ended(); });
    run->session.reset();
    run->connection.reset();
    if (run->lines)
      run->lines->close(Clock::now());
    if (run->process && !run->process->stop(std::chrono::milliseconds::zero()))
      log_.warn("soloist_reap_timeout");
    run->process.reset();
    return run->authFailed;
  }

  void restartLater(Clock::duration ran, Clock::time_point now) {
    const auto& timings = options_.timings;
    if (ran >= timings.steady)
      backoff_ = timings.firstBackoff;
    restartAt_ = now + backoff_;
    backoff_ = std::min(backoff_ * 2, timings.lastBackoff);
  }

  [[nodiscard]] bool wanted() const {
    return settings_ && settings_->enabled && !keyProblem(settings_->apiKey) && !expired_ && !halted_;
  }

  void reconcile(Clock::time_point now) {
    if (restartAt_) {
      if (now < *restartAt_)
        return;
      // due: a past deadline left armed would wake the poll at once, over and over
      restartAt_.reset();
    }
    // the gate opens once a volume held: until then soloist waits, and starts as it opens
    if (!wanted() || run_ || !gateOpen_)
      return;
    startRun(now);
  }

  void startRun(Clock::time_point now) {
    switchAt_.reset();
    const auto build = settings_->dir + "/soloist";
    if (!regularFile(build)) {
      log_.warn("soloist_missing");
      return halt(SoloistState::NoBuild);
    }
    if (!scratch())
      return halt(SoloistState::Failed, "scratch_failed");
    if (!listen())
      return halt(SoloistState::Failed, "pcm_failed");
    auto paths = resolveJail(log_, *settings_, build, options_.resolvConf, *scratch_);
    if (!paths.ok())
      return halt(SoloistState::Failed, paths.error());
    if (!clearPort())
      return halt(SoloistState::Failed, "scratch_failed");
    const auto plan = soloistPlan(paths.value());
    auto version = probed(plan, paths.value().build);
    // a new build's probe may take its time: a command that came meanwhile (a shut gate, the speaker's level) goes
    // first, and the next pass, with the probe cached, starts only if soloist is still wanted and the gate open
    if (waiting())
      return;
    const auto home = kSoloistJailed ? std::string(kJailHome) : scratch_->home;
    const auto serial = ++runs_;
    auto started = std::visit(
        [&](const auto& made) {
          using Plan = std::decay_t<decltype(made)>;
          auto args = soloistArgs(*settings_, home, initialVolume());
          if constexpr (std::is_same_v<Plan, JailPlan>)
            return JailedProcess::start(log_, made, std::move(args),
                                         [this, serial](JailedExit exit) { post(Exited{serial, exit}); });
          else
            return JailedProcess::startPlain(log_, made.plan, plainLine(made.runtime, made.build, std::move(args)),
                                             [this, serial](JailedExit exit) { post(Exited{serial, exit}); });
        },
        plan);
    if (!started.ok()) {
      log_.warn("soloist_start_failed");
      restartLater(Clock::duration::zero(), now);
      return setState(SoloistState::Failed, "spawn_failed");
    }
    // its waits run from the spawn, not from the probe before it
    now = Clock::now();
    auto run = std::make_unique<Run>();
    run->serial = serial;
    run->started = now;
    run->process = std::move(started.value());
    run->output = run->process->takeOutput();
    run->errors = run->process->takeErrors();
    if (options_.logs)
      run->lines.emplace("soloist", options_.logs);
    run->build = std::move(version);
    run->tickAt = now + options_.timings.portTick;
    run_ = std::move(run);
    log_.info("soloist_start", {{"run", serial}});
    setState(SoloistState::Starting);
  }

  // a command or the stop is there to be read
  [[nodiscard]] bool waiting() const {
    std::array<pollfd, 2> fds{{{.fd = commandFd_.get(), .events = POLLIN, .revents = 0},
                               {.fd = stopFd_.get(), .events = POLLIN, .revents = 0}}};
    return ::poll(fds.data(), fds.size(), 0) > 0;
  }

  void halt(SoloistState state, std::string_view code = {}) {
    halted_ = true;
    setState(state, code);
  }

  // once per audiod run and scratch dir
  bool scratch() {
    if (scratch_ && scratch_->root == settings_->scratchDir)
      return true;
    {
      std::scoped_lock lock(pcmMutex_);
      pcm_.reset();
      resolv_.reset();
    }
    scratch_ = prepareSoloistScratch(log_, settings_->scratchDir);
    return scratch_.has_value();
  }

  // the pcm socket lives as long as the scratch: each soloist start connects to it again
  bool listen() {
    std::scoped_lock lock(pcmMutex_);
    if (!pcm_) {
      pcm_ = SoloistPcm::listen(log_, scratch_->run + "/pcm.sock", options_.pcm);
      if (pcm_ && output_ != nullptr)
        pcm_->openOutput(*output_);
    }
    if constexpr (!kSoloistJailed) {
      if (!resolv_)
        resolv_ = SoloistResolv::listen(log_, scratch_->run + "/resolv.sock");
    }
    return pcm_ != nullptr;
  }

  // a port the last soloist left would read as this one's. a ws.port soloist made a directory cannot be removed: home
  // is made again
  bool clearPort() {
    const auto error = soloist::clearPort(scratch_->home);
    if (!error)
      return true;
    log_.warn("soloist_home_reset", {{"call", error->call}, {"errno", error->code}});
    return resetSoloistHome(log_, *scratch_) && !soloist::clearPort(scratch_->home);
  }

  // the build's version, probed again only when the file changed
  std::optional<SoloistBuild> probed(const SoloistPlan& plan, const std::string& build) {
    const auto id = fileId(build);
    if (id && probedId_ == id)
      return probedBuild_;
    const auto probe = probePlan(log_, plan, options_.timings.probeLimit);
    log_.info("soloist_version", {{"code", probeToken(probe)}});
    probedId_ = id;
    probedBuild_ = probe.build;
    return probe.build;
  }

  [[nodiscard]] unsigned initialVolume() const { return audiod::initialVolume(guard_, settings_->initialVolume); }

  void setState(SoloistState state, std::string_view code = {}) {
    status_.state = state;
    status_.code = std::string(code);
  }

  void report() {
    const auto* build = run_ && run_->build ? &*run_->build : nullptr;
    status_.version = build != nullptr ? build->version : std::string();
    status_.buildEpoch = build != nullptr ? build->epoch : 0;
    status_.exitCode = lastExit_;
    status_.silentStarts = silentStarts_;
    if (reported_ && *reported_ == status_)
      return;
    reported_ = status_;
    hooks_.status(status_);
  }

  const Log& log_;
  SoloistHooks hooks_;
  const SoloistOptions options_;
  UniqueFd commandFd_;
  UniqueFd stopFd_;

  std::mutex mutex_;
  std::deque<Command> queue_;

  // the pcm server, which the loop's thread opens and closes the output of
  std::mutex pcmMutex_;
  std::unique_ptr<SoloistPcm> pcm_;
  // audiod's resolv socket, which an unjailed soloist's preload asks (a jailed one resolves through the jail's
  // bound resolv.conf, so a jailed build starts none)
  std::unique_ptr<SoloistResolv> resolv_;
  // the input the output is open on (the session's target), null while it is closed: a pcm socket made again opens
  // on it
  PcmInput* output_ = nullptr;

  // set before the thread starts
  std::shared_ptr<MediaSourceEvents> events_;
  const std::chrono::milliseconds controlAnswer_ = soloist::ControlOptions{}.controlAnswer;

  // the supervisor thread's own
  std::optional<SoloistSettings> settings_;
  // the speaker's level and the first-volume drop, across every run and connection
  SenderGuard guard_;
  Coalescer coalescer_;
  std::optional<Pending> pending_;
  bool gateOpen_ = false;
  std::optional<SoloistScratch> scratch_;
  std::unique_ptr<Run> run_;
  std::uint64_t runs_ = 0;
  std::chrono::milliseconds backoff_;
  std::optional<Clock::time_point> restartAt_;
  // a switch held while music plays, looked at again then
  std::optional<Clock::time_point> switchAt_;
  // its build expired: no start until a switch
  bool expired_ = false;
  // the build, the runtime or the scratch was missing: no start until a new config or a switch
  bool halted_ = false;
  std::uint32_t silentStarts_ = 0;
  std::int32_t lastExit_ = 0;
  std::optional<FileId> probedId_;
  std::optional<SoloistBuild> probedBuild_;
  SoloistStatus status_;
  std::optional<SoloistStatus> reported_;

  // last but the thread: it answers through hooks_
  SoloistProber prober_;
  std::thread thread_;
};

SoloistSupervisor::SoloistSupervisor(const Log& log, SoloistHooks hooks, SoloistOptions options)
    : worker_(std::make_unique<Worker>(log, std::move(hooks), std::move(options))) {}

SoloistSupervisor::~SoloistSupervisor() { stop(); }

void SoloistSupervisor::start() { worker_->start(); }

void SoloistSupervisor::stop() { worker_->stop(); }

void SoloistSupervisor::configure(SoloistSettings settings) { worker_->post(Worker::Configure{std::move(settings)}); }

void SoloistSupervisor::gate(bool open) { worker_->post(Worker::Gate{open}); }

void SoloistSupervisor::probe(std::string requestId, std::string path) {
  worker_->post(Worker::Probe{std::move(requestId), std::move(path)});
}

void SoloistSupervisor::switchBuild() { worker_->post(Worker::Switch{}); }

void SoloistSupervisor::events(std::shared_ptr<MediaSourceEvents> events) { worker_->events(std::move(events)); }

void SoloistSupervisor::control(soloist::Command command, ControlAnswer answer) {
  worker_->post(Worker::Control{command, std::move(answer)});
}

void SoloistSupervisor::speakerLevel(std::uint8_t level) { worker_->post(Worker::Level{level}); }

void SoloistSupervisor::openOutput(PcmInput& music) { worker_->openOutput(music); }

void SoloistSupervisor::closeOutput() { worker_->closeOutput(); }

}  // namespace cadence::audiod

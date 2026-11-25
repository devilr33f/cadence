#include "audiod/airplay-stack.h"

#include "audiod/airplay-config.h"
#include "audiod/airplay-files.h"
#include "dbus/auth.h"
#include "dbus/bus.h"

#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <filesystem>
#include <utility>

namespace cadence::audiod {

namespace {

using Clock = std::chrono::steady_clock;

// soloist's, as v1 ran its helper and every child with it (reference §2.4)
constexpr int kNice = 5;
constexpr std::string_view kOomScoreAdj = "500";
// a question to the private bus, which answers at once
constexpr std::chrono::milliseconds kNameCall{250};
constexpr std::array<std::string_view, 4> kPrograms{"dbus-daemon", "avahi-daemon", "nqptp", "shairport-sync"};

// the programs an attempt runs: all of them, but for avahi and nqptp in a sim build that leaves them out
bool runs(std::string_view program) { return kAirplayLan || (program != "avahi-daemon" && program != "nqptp"); }

bool executable(const std::string& path) {
  struct stat info {};
  return ::lstat(path.c_str(), &info) == 0 && S_ISREG(info.st_mode) && (info.st_mode & 0111) != 0;
}

bool socketAt(const std::string& path) {
  struct stat info {};
  return ::lstat(path.c_str(), &info) == 0 && S_ISSOCK(info.st_mode);
}

// a daemon's lines go under the name cadenced knows it by
std::string_view lineSource(std::string_view daemon) { return daemon == "shairport" ? "shairport-sync" : daemon; }

}  // namespace

AirplayStack::AirplayStack(const Log& log, AirplaySettings settings, const AirplayOptions& options,
                           AirplayOutput& output, Hooks hooks)
    : log_(log), settings_(std::move(settings)), options_(options), output_(output), hooks_(std::move(hooks)) {}

AirplayStack::~AirplayStack() { stop(); }

std::string_view AirplayStack::start() {
  for (const auto step : {&AirplayStack::prepare, &AirplayStack::startBus, &AirplayStack::startAvahi,
                          &AirplayStack::startTiming, &AirplayStack::startReceiver}) {
    if (const auto code = (this->*step)(); !code.empty())
      return code;
  }
  return {};
}

std::string_view AirplayStack::prepare() {
  const auto failed = [&](std::string_view step, const SysError& error) {
    log_.warn("airplay_scratch_failed", {{"step", step}, {"call", error.call}, {"errno", error.code}});
    return airplay::kScratchFailed;
  };
  runtime_ = settings_.dir + "/runtime";
  loader_.clear();
  for (const auto loader : airplay::kLoaders)
    if (loader_.empty() && executable(runtime_ + "/lib/" + std::string(loader)))
      loader_ = runtime_ + "/lib/" + std::string(loader);
  bool present = settings_.dir.starts_with('/') && !loader_.empty();
  for (const auto program : kPrograms)
    present = present && (!runs(program) || executable(runtime_ + "/bin/" + std::string(program)));
  if (!present) {
    log_.warn("airplay_runtime_missing");
    return airplay::kRuntimeMissing;
  }
  const auto id = airplay::receiverId(options_.interfaces);
  if (!id) {
    log_.warn("airplay_no_interface");
    return airplay::kNoInterface;
  }
  receiverId_ = *id;

  const auto& scratch = settings_.scratchDir;
  const auto socket = scratch + "/bus.sock";
  if (!scratch.starts_with('/') || socket.size() >= sizeof(sockaddr_un::sun_path))
    return failed("scratch", {.call = "path", .code = EINVAL});
  for (const auto& dir : {scratch, scratch + "/run"})
    if (const auto error = airplay::privateDir(dir))
      return failed("scratch", *error);
  if (const auto error = airplay::freshDir(scratch + "/empty"))
    return failed("scratch", *error);
  if (const auto error = airplay::removeStale(socket))
    return failed("scratch", *error);
  // avahi reads no service or hosts file while etc/ is absent, and finds no stale pid file
  if constexpr (kAirplayLan) {
    const auto& base = options_.avahiBase;
    if (const auto error = airplay::privateDir(std::filesystem::path(base).parent_path().string()))
      return failed("avahi", *error);
    if (const auto error = airplay::freshDir(base))
      return failed("avahi", *error);
    if (const auto error = airplay::privateDir(base + "/run"))
      return failed("avahi", *error);
  }

  const auto shairport = airplay::shairportConfig(settings_.name, receiverId_, scratch + "/run/audio.fifo",
                                                  scratch + "/run/metadata.fifo");
  if (!shairport) {
    log_.warn("airplay_invalid_name");
    return airplay::kInvalidName;
  }
  std::vector<std::pair<std::string, std::string>> configs{
      {scratch + "/dbus.conf", airplay::busConfig(socket, ::geteuid())}, {scratch + "/shairport.conf", *shairport}};
  if constexpr (kAirplayLan)
    configs.emplace_back(scratch + "/avahi.conf", airplay::avahiConfig(receiverId_));
  for (const auto& [path, text] : configs)
    if (const auto error = airplay::writePrivate(path, text))
      return failed("config", *error);
  address_ = dbus::unixAddress(socket);
  return {};
}

std::string_view AirplayStack::startBus() {
  const auto& scratch = settings_.scratchDir;
  if (const auto code = spawn("dbus", "dbus-daemon",
                              {"--nofork", "--nosyslog", "--nopidfile", "--config-file=" + scratch + "/dbus.conf"},
                              false);
      !code.empty())
    return code;
  const auto socket = scratch + "/bus.sock";
  if (const auto waited = waitFor(options_.timings.resourceWait, [&] { return socketAt(socket); });
      waited != Waited::Ready)
    return failure(waited);
  auto connected = dbus::Client::connect(address_, {.connectTimeout = options_.timings.resourceWait,
                                                    .sendTimeout = dbus::kDefaultCallTimeout,
                                                    .stopFd = hooks_.stopFd,
                                                    .readBudget = dbus::kReadBudget});
  if (!connected.ok()) {
    if (connected.error() == dbus::ConnectError::Stopped)
      return airplay::kInterrupted;
    log_.warn("airplay_bus_failed", {{"reason", dbus::connectErrorToken(connected.error())}});
    return airplay::kBusFailed;
  }
  client_.emplace(std::move(connected.value()));
  return {};
}

std::optional<bool> AirplayStack::hasOwner(std::string_view name) {
  const auto reply = client_->callAndWait(dbus::nameHasOwnerCall(std::string(name)), kNameCall);
  if (reply.status != dbus::CallStatus::Ok || reply.body.size() != 1)
    return std::nullopt;
  return reply.body.front().asBoolean();
}

std::string_view AirplayStack::startAvahi() {
  if constexpr (!kAirplayLan)
    return {};
  const auto before = hasOwner(airplay::kAvahiName);
  if (!before)
    return airplay::kBusFailed;
  // nothing of ours owns it yet: whoever does is foreign, and never adopted
  if (*before) {
    log_.warn("airplay_foreign_service", {{"name", "avahi"}});
    return airplay::kForeignService;
  }
  const auto& scratch = settings_.scratchDir;
  if (const auto code =
          spawn("avahi", "avahi-daemon",
                {"--file=" + scratch + "/avahi.conf", "--no-drop-root", "--no-chroot", "--no-rlimits"}, true);
      !code.empty())
    return code;
  return failure(waitFor(options_.timings.resourceWait, [&] { return hasOwner(airplay::kAvahiName) == true; }));
}

std::string_view AirplayStack::startTiming() {
  if constexpr (!kAirplayLan)
    return {};
  const auto& path = options_.timingPath;
  const auto previous = airplay::timingStamp(path);
  {
    // held while the stale interface goes, so a foreign timing daemon fails the attempt before shairport could talk to
    // it, and none can be running meanwhile (v1). let go before the spawn: nqptp binds the same ports itself
    auto hold = airplay::PortHold::bind(options_.ports);
    if (!hold.ok()) {
      log_.warn("airplay_ports_busy", {{"errno", hold.error().code}});
      return airplay::kPortsBusy;
    }
    if (const auto error = airplay::removeStaleTiming(path)) {
      log_.warn("airplay_foreign_service", {{"name", "timing"}, {"call", error->call}, {"errno", error->code}});
      return airplay::kForeignService;
    }
  }
  if (const auto code = spawn("nqptp", "nqptp", {}, false); !code.empty())
    return code;
  const auto nudge = airplay::TimingNudge::open();
  if (!nudge)
    return airplay::kSpawnFailed;
  return failure(waitFor(options_.timings.resourceWait, [&] {
    if (airplay::timingFresh(path, previous))
      return true;
    nudge->send(options_.ports.control);
    return false;
  }));
}

std::string_view AirplayStack::startReceiver() {
  const auto& scratch = settings_.scratchDir;
  const auto audio = scratch + "/run/audio.fifo";
  const auto metadata = scratch + "/run/metadata.fifo";
  for (const auto& [name, path] : {std::pair{"audio", audio}, std::pair{"metadata", metadata}}) {
    if (const auto error = airplay::privateFifo(path)) {
      log_.warn("airplay_scratch_failed", {{"step", name}, {"call", error->call}, {"errno", error->code}});
      return airplay::kScratchFailed;
    }
  }
  // a reader is there before shairport opens either
  pipe_ = AirplayPipe::start(log_, audio, metadata, output_, hooks_.metadata, hooks_.progress, scratch + "/run/artwork");
  if (!pipe_)
    return airplay::kSpawnFailed;

  const auto before = hasOwner(airplay::kShairportName);
  if (!before)
    return airplay::kBusFailed;
  if (*before) {
    log_.warn("airplay_foreign_service", {{"name", "shairport"}});
    return airplay::kForeignService;
  }
  // before the name is taken. a change of its owner that the waits below dispatch with no handler yet is caught by the
  // supervisor's check (stillOwner) once its handler is in
  const auto rule = dbus::nameOwnerChangedRule(airplay::kShairportName);
  if (!rule || client_->callAndWait(dbus::addMatchCall(*rule), kNameCall).status != dbus::CallStatus::Ok)
    return airplay::kBusFailed;
  if (const auto code = spawn("shairport", "shairport-sync", {"--configfile", scratch + "/shairport.conf"}, true);
      !code.empty())
    return code;
  return failure(waitFor(options_.timings.receiverWait, [&] {
    auto owner = shairportOwner();
    if (!owner)
      return false;
    owner_ = std::move(*owner);
    return true;
  }));
}

std::optional<std::string> AirplayStack::shairportOwner() {
  const auto reply = client_->callAndWait(dbus::getNameOwnerCall(std::string(airplay::kShairportName)), kNameCall);
  const auto* owner =
      reply.status == dbus::CallStatus::Ok && reply.body.size() == 1 ? reply.body.front().asString() : nullptr;
  if (owner == nullptr || !owner->starts_with(':'))
    return std::nullopt;
  return *owner;
}

bool AirplayStack::stillOwner() { return client_ && client_->open() && shairportOwner() == owner_; }

std::string_view AirplayStack::spawn(std::string_view name, std::string_view program, std::vector<std::string> args,
                                     bool bus) {
  const auto& scratch = settings_.scratchDir;
  std::vector<std::string> env{"PATH=/nonexistent", "HOME=" + scratch, "GIO_MODULE_DIR=" + scratch + "/empty",
                               "GIO_USE_VFS=local", "GCONV_PATH=" + scratch + "/empty"};
  if (bus)
    env.push_back("DBUS_SYSTEM_BUS_ADDRESS=" + address_);
  std::vector<std::string> line{"--library-path", runtime_ + "/lib", runtime_ + "/bin/" + std::string(program)};
  line.insert(line.end(), std::make_move_iterator(args.begin()), std::make_move_iterator(args.end()));
  const bool forward = static_cast<bool>(options_.logs);
  const PlainPlan plan{.program = loader_,
                       .env = std::move(env),
                       .nice = kNice,
                       .oomScoreAdj = std::string(kOomScoreAdj),
                       .output = forward};
  auto started = JailedProcess::startPlain(log_, plan, std::move(line), [exited = exited_, hook = hooks_.exited](
                                                                             JailedExit /*exit*/) {
    exited->store(true);
    if (hook)
      hook();
  });
  if (!started.ok()) {
    log_.warn("airplay_spawn_failed", {{"daemon", name}});
    return airplay::kSpawnFailed;
  }
  log_.info("airplay_daemon_started", {{"daemon", name}, {"pid", started.value()->pid()}});
  if (forward) {
    if (!lines_)
      lines_ = std::make_unique<ChildLogReader>(options_.logs);
    lines_->add(lineSource(name), started.value()->takeOutput(), started.value()->takeErrors());
  }
  daemons_.push_back({.name = name, .process = std::move(started.value())});
  return {};
}

AirplayStack::Waited AirplayStack::waitFor(std::chrono::milliseconds limit, const std::function<bool()>& ready) {
  const auto deadline = Clock::now() + limit;
  for (;;) {
    if (ready())
      return Waited::Ready;
    if (exited())
      return Waited::Exited;
    const auto now = Clock::now();
    if (now >= deadline)
      return Waited::Timeout;
    const auto left = std::chrono::ceil<std::chrono::milliseconds>(deadline - now);
    if (!hooks_.idle(std::min(options_.timings.tick, left)))
      return Waited::Interrupted;
  }
}

std::string_view AirplayStack::failure(Waited waited) const {
  switch (waited) {
    case Waited::Ready:
      return {};
    case Waited::Timeout:
      log_.warn("airplay_timeout", {{"daemon", daemons_.empty() ? std::string_view("none") : daemons_.back().name}});
      return airplay::kTimeout;
    case Waited::Exited:
      log_.warn("airplay_exited", {{"daemon", daemons_.empty() ? std::string_view("none") : daemons_.back().name}});
      return airplay::kExited;
    case Waited::Interrupted:
      return airplay::kInterrupted;
  }
  return airplay::kTimeout;
}

std::vector<std::unique_ptr<JailedProcess>> AirplayStack::stop() {
  std::vector<std::unique_ptr<JailedProcess>> unreaped;
  client_.reset();
  owner_.clear();
  while (!daemons_.empty()) {
    auto daemon = std::move(daemons_.back());
    daemons_.pop_back();
    // the two that phones see say goodbye on their lan (mdns); the rest have nothing to say
    const bool seen = daemon.name == "shairport" || daemon.name == "avahi";
    if (!daemon.process->stop(seen ? options_.timings.stopGrace : std::chrono::milliseconds::zero())) {
      log_.warn("airplay_reap_timeout", {{"daemon", daemon.name}});
      unreaped.push_back(std::move(daemon.process));
    }
    // shairport, the writer, is gone: its reader goes next
    if (daemon.name == "shairport")
      pipe_.reset();
  }
  pipe_.reset();
  // what the daemons printed up to their end, and the lines they did not end
  lines_.reset();
  return unreaped;
}

}  // namespace cadence::audiod

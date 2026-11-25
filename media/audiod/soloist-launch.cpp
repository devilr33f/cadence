#include "audiod/soloist-launch.h"

#include "audiod/jailed-process.h"

#include <sys/stat.h>

#include <array>
#include <cstdlib>
#include <memory>

namespace cadence::audiod {

namespace {

constexpr std::size_t kMaxKey = 256;
// soloist's exit when its exec failed under an emulation layer that reports it so
constexpr int kExecFailed = 127;

// realpath, or empty when the path does not resolve
std::string resolved(const std::string& path) {
  if (path.empty())
    return {};
  const std::unique_ptr<char, decltype(&std::free)> real(::realpath(path.c_str(), nullptr), &std::free);
  return real ? std::string(real.get()) : std::string();
}

bool regularFile(const std::string& path) {
  struct stat info {};
  return ::stat(path.c_str(), &info) == 0 && S_ISREG(info.st_mode);
}

bool executableFile(const std::string& path) {
  struct stat info {};
  return ::stat(path.c_str(), &info) == 0 && S_ISREG(info.st_mode) && (info.st_mode & S_IXUSR) != 0;
}

// the loaders and library directories a runtime may bring (assemble-soloist-runtime.sh), one row per cpu: the loader
// where soloist's PT_INTERP names it, its libraries under lib/<triple> and usr/lib/<triple> without a cache, and the
// resolv preload in usr/lib/<triple>. a 32-bit arm audiod (midi, android) runs the arm64 row: the kernel is 64-bit
struct RuntimeArch {
  const char* loader;
  const char* triple;
};

constexpr std::array kRuntimeArches{RuntimeArch{.loader = "lib/ld-linux-aarch64.so.1", .triple = "aarch64-linux-gnu"},
                                    RuntimeArch{.loader = "lib64/ld-linux-x86-64.so.2", .triple = "x86_64-linux-gnu"}};

constexpr std::string_view kResolvPreload = "libcadence-resolv.so";

// a capture's answer as a probe's: io when it did not run or timed out, version when it says nothing soloist-like
SoloistProbe probeOf(const Result<Captured, JailError>& captured) {
  if (!captured.ok() || captured.value().timedOut)
    return {.code = "io", .build = std::nullopt};
  const auto& output = captured.value().output;
  const auto& exit = captured.value().exit;
  if (output.empty() && exit && exit->code == kExecFailed)
    return {.code = "io", .build = std::nullopt};
  auto build = parseSoloistVersion(output);
  if (!build)
    return {.code = "version", .build = std::nullopt};
  return {.code = {}, .build = std::move(build)};
}

}  // namespace

std::optional<std::string_view> keyProblem(std::string_view key) {
  if (key.empty())
    return "missing";
  if (key.size() > kMaxKey)
    return "invalid";
  for (const char c : key)
    if (c < 0x21 || c > 0x7e)
      return "invalid";
  return std::nullopt;
}

std::vector<std::string> soloistArgs(const SoloistSettings& settings, const std::string& home, unsigned initialVolume) {
  return {"--device-name",
          settings.deviceName.empty() ? std::string(kDefaultDeviceName) : settings.deviceName,
          "--api-key",
          settings.apiKey,
          "--data-dir",
          home,
          "--cache-dir",
          home + "/cache",
          "--cache-size",
          "100",
          "--initial-volume",
          std::to_string(initialVolume),
          "--ws",
          "127.0.0.1:0"};
}

Result<JailPaths, std::string_view> resolveJail(const Log& log, const SoloistSettings& settings,
                                                const std::string& build, const std::string& resolvConf,
                                                const SoloistScratch& scratch) {
  const auto unavailable = [&](std::string_view code, std::string_view reason, std::string_view mount) {
    log.warn("soloist_unavailable", {{"reason", reason}, {"mount", mount}});
    return code;
  };
  JailPaths paths{.runtimeRoot = resolved(settings.dir + "/runtime"),
                  .build = resolved(build),
                  .resolvConf = resolved(resolvConf),
                  .caFile = resolved(settings.caFile),
                  .home = resolved(scratch.home),
                  .run = resolved(scratch.run)};
  if (paths.caFile.empty() || !regularFile(paths.caFile))
    return unavailable("ca_missing", "ca", "ca");
  if (paths.runtimeRoot.empty())
    return unavailable("runtime_missing", "runtime", "root");
  // an unjailed build (android) mounts nothing: the jail's mount points (proc, dev) are not in its runtime, and only
  // the paths the plain plan uses need to exist
  if constexpr (!kSoloistJailed)
    return paths;
  if (const auto refusal = checkJail(planJail(paths)))
    return refusal->mount == "ca" ? unavailable("ca_missing", "ca", refusal->mount)
                                  : unavailable("runtime_missing", "runtime", refusal->mount);
  return paths;
}

SoloistProbe probeSoloist(const Log& log, const JailPlan& plan, std::chrono::milliseconds limit) {
  return probeOf(JailedProcess::runCapture(log, plan, {"--version"}, limit, kCaptureBytes));
}

RuntimeLaunch findRuntime(const std::string& runtimeRoot) {
  if (runtimeRoot.empty())
    return {};
  const std::string root = resolved(runtimeRoot);
  if (root.empty())
    return {};
  for (const auto& arch : kRuntimeArches) {
    const std::string loader = root + "/" + arch.loader;
    if (!executableFile(loader))
      continue;
    const std::string triple = root + "/lib/" + arch.triple;
    const std::string usr = root + "/usr/lib/" + arch.triple;
    RuntimeLaunch out{.loader = loader, .libraryPath = triple + ":" + usr, .preload = {}};
    if (regularFile(usr + "/" + std::string(kResolvPreload)))
      out.preload = usr + "/" + std::string(kResolvPreload);
    return out;
  }
  return {};
}

SoloistPlainPlan planPlain(const JailPaths& paths) {
  const RuntimeLaunch runtime = findRuntime(paths.runtimeRoot);
  std::vector<std::string> env{"HOME=" + paths.home,
                               "SPOTIFY_SSL_CERT_FILE=" + paths.caFile,
                               "PULSE_SERVER=unix:" + paths.run + "/pcm.sock",
                               "PATH=/nonexistent",
                               "TMPDIR=" + paths.home};
  if (!runtime.preload.empty()) {
    env.push_back("LD_PRELOAD=" + runtime.preload);
    // the resolv socket the preload forwards soloist's lookups over (audiod serves it beside the pcm's)
    env.push_back("CADENCE_RESOLV_SOCK=" + paths.run + "/resolv.sock");
  }
  return {.plan = PlainPlan{.program = runtime.loader.empty() ? paths.build : runtime.loader,
                            .env = std::move(env),
                            .nice = kSoloistNice,
                            .oomScoreAdj = std::string(kSoloistOomScoreAdj),
                            .output = true},
          .runtime = runtime,
          .build = paths.build};
}

std::vector<std::string> plainLine(const RuntimeLaunch& runtime, const std::string& build,
                                   std::vector<std::string> args) {
  std::vector<std::string> line;
  if (!runtime.loader.empty()) {
    line.push_back("--library-path");
    line.push_back(runtime.libraryPath);
    line.push_back(build);
  }
  line.insert(line.end(), std::make_move_iterator(args.begin()), std::make_move_iterator(args.end()));
  return line;
}

SoloistProbe probePlainSoloist(const Log& log, const SoloistPlainPlan& plan, const std::vector<std::string>& line,
                               std::chrono::milliseconds limit) {
  return probeOf(JailedProcess::runCapturePlain(log, plan.plan, line, limit, kCaptureBytes));
}

SoloistPlan soloistPlan(const JailPaths& paths) {
  if constexpr (kSoloistJailed)
    return planJail(paths);
  else
    return planPlain(paths);
}

SoloistProbe probePlan(const Log& log, const SoloistPlan& plan, std::chrono::milliseconds limit) {
  return std::visit(
      [&](const auto& made) -> SoloistProbe {
        using Plan = std::decay_t<decltype(made)>;
        if constexpr (std::is_same_v<Plan, JailPlan>)
          return probeSoloist(log, made, limit);
        else
          return probePlainSoloist(log, made, plainLine(made.runtime, made.build, {"--version"}), limit);
      },
      plan);
}

SoloistProber::SoloistProber(const Log& log, Done done, std::chrono::milliseconds limit)
    : log_(log), done_(std::move(done)), limit_(limit) {}

SoloistProber::~SoloistProber() { stop(); }

void SoloistProber::start() {
  if (!thread_.joinable())
    thread_ = std::thread([this] { run(); });
}

void SoloistProber::stop() {
  {
    std::scoped_lock lock(mutex_);
    stopping_ = true;
    jobs_.clear();
  }
  changed_.notify_all();
  if (thread_.joinable())
    thread_.join();
}

void SoloistProber::probe(std::string requestId, SoloistPlan plan) {
  {
    std::scoped_lock lock(mutex_);
    if (stopping_)
      return;
    jobs_.push_back({std::move(requestId), std::move(plan)});
  }
  changed_.notify_all();
}

void SoloistProber::run() {
  for (;;) {
    Job job;
    {
      std::unique_lock lock(mutex_);
      changed_.wait(lock, [this] { return stopping_ || !jobs_.empty(); });
      if (stopping_)
        return;
      job = std::move(jobs_.front());
      jobs_.pop_front();
    }
    const auto probe = probePlan(log_, job.plan, limit_);
    log_.info("soloist_probe", {{"code", probe.code.empty() ? std::string_view("ok") : probe.code}});
    {
      // a stop that began meanwhile wants nothing more answered
      std::scoped_lock lock(mutex_);
      if (stopping_)
        return;
    }
    done_(job.requestId, probe);
  }
}

}  // namespace cadence::audiod

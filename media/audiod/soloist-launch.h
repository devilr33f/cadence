#pragma once

#include "audiod/jailed-process.h"
#include "audiod/log.h"
#include "audiod/soloist-jail.h"
#include "audiod/soloist-scratch.h"
#include "audiod/soloist-version.h"
#include "result.h"

#include <chrono>
#include <condition_variable>
#include <deque>
#include <functional>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <thread>
#include <variant>
#include <vector>

namespace cadence::audiod {

// what cadenced's SoloistConfig asks for (M4c-D11)
struct SoloistSettings {
  bool enabled = false;
  // secret: never logged, never in a status or an error; it reaches soloist only on its command line
  std::string apiKey;
  // soloist's name on the lan; empty for kDefaultDeviceName
  std::string deviceName;
  // the board's soloist dir: the installed build at <dir>/soloist, the runtime at <dir>/runtime
  std::string dir;
  // soloist's scratch, recreated once per audiod run
  std::string scratchDir;
  // the ca bundle soloist verifies spotify against
  std::string caFile;
  // --initial-volume when the speaker's level is not known yet
  unsigned initialVolume = 50;

  bool operator==(const SoloistSettings&) const = default;
};

inline constexpr std::string_view kDefaultDeviceName = "Cadence";

// why a key cannot be used, as v1's key.rs judged one: missing (empty) or invalid (over 256 bytes, or a byte outside
// 0x21-0x7e); nullopt when it can
std::optional<std::string_view> keyProblem(std::string_view key);

// soloist's command line (reference §1.4), the key in it: never logged. home is its data dir: the jail's
// /home/soloist, or in a jail-off build the scratch's home itself, as nothing is bound there
std::vector<std::string> soloistArgs(const SoloistSettings& settings, const std::string& home, unsigned initialVolume);

// the host paths a jail of the build at build is made of, each through realpath, so a symlink on the way (a board's
// /etc/resolv.conf, a ca bundle) is resolved rather than refused, then checked as the child will need them. the error
// is the start's code: ca_missing, or runtime_missing for anything else; logged as soloist_unavailable reason=…
// mount=…
Result<JailPaths, std::string_view> resolveJail(const Log& log, const SoloistSettings& settings,
                                                const std::string& build, const std::string& resolvConf,
                                                const SoloistScratch& scratch);

// a probe's answer: code empty with the build, or version (its output does not parse) or io (it could not run, or did
// not answer within the limit)
struct SoloistProbe {
  std::string_view code;
  std::optional<SoloistBuild> build;
};

// <plan's build> --version in the jail, stdout only and within limit (reference §6.2). blocks its caller up to limit
// and the reap wait
SoloistProbe probeSoloist(const Log& log, const JailPlan& plan, std::chrono::milliseconds limit);

// what a plain launch takes from the runtime (assemble-soloist-runtime.sh's layout): the loader it brings, the
// libraries beside it and the resolv shim, each empty when the runtime has none of that
struct RuntimeLaunch {
  // runtime/lib/<the cpu's loader>; empty when absent, and the build then runs as itself (the sim's fake soloist)
  std::string loader;
  // --library-path's value, the runtime's two library directories; empty with no loader
  std::string libraryPath;
  // runtime/usr/lib/<triple>/libcadence-resolv.so, the preload that forwards soloist's lookups to audiod
  // (media/audiod/soloist-resolv.h); empty when the runtime has none
  std::string preload;
};

// the runtime's loader, libraries and resolv shim, from runtimeRoot as the plan's paths resolved it. an arm64 build
// on a 32-bit-only kernel cannot run however it is launched: that shows at the exec, not here
RuntimeLaunch findRuntime(const std::string& runtimeRoot);

// what a plain plan holds beside the exec's own words: the runtime its loader came from, for the command line, and
// the build the line names after the loader's own words
struct SoloistPlainPlan {
  PlainPlan plan;
  RuntimeLaunch runtime;
  std::string build;
};

// soloist unjailed (an audiod in the android app: no namespace, no chroot, no ids, the app's own uid): through the
// runtime's loader with --library-path when the runtime brings one, else the build itself. its audio reaches the pcm
// socket through the pulse shim, which PULSE_SERVER points at the scratch's own; its ca bundle and home are the
// host's paths, and the resolv preload, when the runtime has one, is told the scratch's resolv socket
SoloistPlainPlan planPlain(const JailPaths& paths);

// a plain start's command line after argv[0], which is the plan's program: the loader's words and the build it loads,
// then the args the caller made of the settings; the args alone when the build runs as itself
std::vector<std::string> plainLine(const RuntimeLaunch& runtime, const std::string& build,
                                   std::vector<std::string> args);

// a plain build's probe: line is plainLine's with --version as the args
SoloistProbe probePlainSoloist(const Log& log, const SoloistPlainPlan& plan, const std::vector<std::string>& line,
                               std::chrono::milliseconds limit);

// the plan a build makes of the resolved paths: the jail's, or the plain one an unjailed build runs (the sim's fake
// soloist, android's real one through the runtime's loader)
using SoloistPlan = std::variant<JailPlan, SoloistPlainPlan>;

SoloistPlan soloistPlan(const JailPaths& paths);

// the plan's own --version, as its build would run it
SoloistProbe probePlan(const Log& log, const SoloistPlan& plan, std::chrono::milliseconds limit);

// cadenced's probes, one at a time on a long-lived thread of their own: a --version may take up to the limit and the
// reap wait, and the supervisor's thread must read soloist's websocket on time meanwhile (rule 7 judges an echo by
// when it came). each answer goes to done on this thread, logged soloist_probe code=…
class SoloistProber {
 public:
  using Done = std::function<void(const std::string& requestId, const SoloistProbe& probe)>;

  SoloistProber(const Log& log, Done done, std::chrono::milliseconds limit);
  // stops it
  ~SoloistProber();

  SoloistProber(const SoloistProber&) = delete;
  SoloistProber& operator=(const SoloistProber&) = delete;

  void start();
  // bounded by a probe under way; the probes still queued are dropped, and nothing is answered after it returns
  void stop();
  // any thread
  void probe(std::string requestId, SoloistPlan plan);

 private:
  struct Job {
    std::string requestId;
    SoloistPlan plan;
  };

  void run();

  const Log& log_;
  Done done_;
  const std::chrono::milliseconds limit_;
  std::mutex mutex_;
  std::condition_variable changed_;
  std::deque<Job> jobs_;
  bool stopping_ = false;
  std::thread thread_;
};

}  // namespace cadence::audiod

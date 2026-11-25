#pragma once

#include "audio/stock-api.h"
#include "audio/stock-library.h"
#include "audio/wake-gate.h"
#include "audiod/jailed-process.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "result.h"

#include "cadence/audiod/v1/audiod.pb.h"

#ifdef CADENCE_MEDIA_KWS
#include "audiod/keyword-model.h"
#endif

#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <functional>
#include <map>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::audiod {

// how long a probe child may take before the engines count as hanging. cadenced holds a session's first packet for
// two of them and the engines' start while the vendor status is pending (vendorProbeLimit in
// device/internal/audiod/supervisor.go, kept in step by a test there)
inline constexpr std::chrono::milliseconds kProbeLimit = std::chrono::seconds(5);
// how long the engines may take to start once their probes passed. past it audiod exits with kExitEnginesHung rather
// than leave capture waiting on a library that hangs: the thread that holds the engines and the device cannot be
// abandoned, and cadenced's supervisor restarts audiod, counts the crash and turns the engines off after three
inline constexpr std::chrono::milliseconds kEngineStartLimit = std::chrono::seconds(15);
inline constexpr int kExitEnginesHung = 4;
// how soon after a wake a session takes the vqe output heard since it
inline constexpr std::chrono::milliseconds kWakePreroll = std::chrono::seconds(1);
// the probe child's exit when the vqe opened but the spotter refused cleanly: the vqe still serves button sessions
inline constexpr int kProbeSpotterFailed = 3;
// cadence-audiod --probe-vendor <capture section, hex> [--commands]
inline constexpr std::string_view kProbeFlag = "--probe-vendor";
inline constexpr std::string_view kProbeCommandsFlag = "--commands";

// the program audiod runs as its probe child, with exactly this environment
struct ProbeProgram {
  std::string path;
  std::vector<std::string> env;
};

// cadence-audiod itself, in probe mode, in its own environment
ProbeProgram selfProbe();

// how audiod runs the board's vendor engines and keyword model; tests swap in the fake library, the fake probe child
// and a scripted model
struct VendorOptions {
  StockLibraryLoader loader = openStockLibrary;
  ProbeProgram probe = selfProbe();
  std::chrono::milliseconds probeLimit = kProbeLimit;
  std::uint64_t wakeWarmupMs = kWakeWarmupMs;
  std::chrono::milliseconds wakePreroll = kWakePreroll;
  std::chrono::milliseconds startLimit = kEngineStartLimit;
  // how audiod ends when the engines' start ran past startLimit: at once, since nothing may wait for that thread
  std::function<void(int code)> exit = [](int code) { std::_Exit(code); };
#ifdef CADENCE_MEDIA_KWS
  // builds the board's keyword model from the checked bytes of its file
  KeywordModelLoader keywordModel = loadTflmKeyword;
  // the frames the keyword spotter may lag behind the capture; tests that hand audio faster than real time go deeper
  std::size_t keywordQueueFrames = kKeywordQueueFrames;
#endif
};

// the engines a Configure names, as found on the device: the stock config they start with (commandWords set when a
// command model and words to match are named and the model is there) and why command words are off although asked
// for (command_model_missing)
struct VendorSetup {
  StockAudioConfig stock;
  std::string_view commandCode;
};

// files only, the library is never loaded: library_missing (no root holds it), model_missing, layout_invalid. every
// path, name and the layout come from the Configure
Result<VendorSetup, std::string_view> resolveVendor(const cadence_audiod_v1_CaptureConfig& config,
                                                    std::uint64_t warmupMs);

// the probe child's arguments: the flag, the capture section and whether to open command words too
std::vector<std::string> probeArgs(const cadence_audiod_v1_CaptureConfig& config, bool commands);

// the probe child (argv[1] is kProbeFlag): opens the library, the vqe and the spotter, with command words when asked,
// exactly as the engine will, and feeds them nothing. a library that quick_exits or aborts on what it dislikes ends
// only this child. 0 when everything opened, kProbeSpotterFailed when only the spotter (without command words)
// refused, 1 when anything else failed, 2 for arguments it cannot read
int vendorProbeMain(std::span<char* const> argv, const StockLibraryLoader& loader);

enum class ProbeVerdict : std::uint8_t { Passed, SpotterFailed, Failed, TimedOut };

struct ProbeOutcome {
  ProbeVerdict verdict = ProbeVerdict::Failed;
  // how it ended; a child that never started or was killed at the limit has none
  std::optional<JailedExit> exit;
};

// one probe child, unjailed through a jail's child sequence (signals reset, no new privileges, the death signal, no
// descriptor of audiod's). it passes by exiting 0 and keeps the vqe alone by exiting kProbeSpotterFailed; any other
// exit, a signal or the limit fail it, and one still running then is killed. done runs once on the loop, unless the probe is destroyed first, which kills the child
class VendorProbe {
 public:
  using Done = std::function<void(ProbeOutcome)>;

  static std::unique_ptr<VendorProbe> start(Loop& loop, const Log& log, const ProbeProgram& program,
                                            std::vector<std::string> args, std::chrono::milliseconds limit, Done done);
  ~VendorProbe();

  VendorProbe(const VendorProbe&) = delete;
  VendorProbe& operator=(const VendorProbe&) = delete;

 private:
  VendorProbe(Loop& loop, Done done);

  // done may destroy the probe: nothing of it is touched after
  void finish(ProbeOutcome outcome);

  Loop& loop_;
  Done done_;
  // expires with the probe, so what the child's thread posted after that is dropped
  std::shared_ptr<char> alive_ = std::make_shared<char>();
  Loop::TimerId timer_ = 0;
  std::unique_ptr<JailedProcess> process_;
  bool finished_ = false;
};

// whether the vendor engines a Configure names may run, and with command words
struct VendorVerdict {
  // the engines start with this; commandWords only when the command model's own probe passed too
  std::optional<VendorSetup> setup;
  // the VendorStatus code: why the engines are off, else why command words are, else empty
  std::string_view code;
};

// the files, then the probe children: the engines alone, then with command words. a spotter refused cleanly keeps
// the vqe without wake or command words (spotter_failed). a probe's verdict holds for the audiod run (a hanging or
// crashing library is not tried again), per probe input. loop thread only
class VendorResolver {
 public:
  using Done = std::function<void(VendorVerdict)>;

  VendorResolver(Loop& loop, const Log& log, const VendorOptions& options);

  // done runs on the loop: before this returns when the files or known verdicts decide, else once a probe ended. a
  // resolve still running is cancelled
  void resolve(const cadence_audiod_v1_CaptureConfig& config, Done done);
  // kills a running probe; its done never runs
  void cancel();

 private:
  void probe(bool commands);
  void probed(bool commands, ProbeVerdict verdict);
  void finish(VendorVerdict verdict);

  Loop& loop_;
  const Log& log_;
  const VendorOptions& options_;
  std::map<std::string, ProbeVerdict> verdicts_;
  std::optional<cadence_audiod_v1_CaptureConfig> config_;
  std::optional<VendorSetup> setup_;
  std::unique_ptr<VendorProbe> probe_;
  Done done_;
};

}  // namespace cadence::audiod

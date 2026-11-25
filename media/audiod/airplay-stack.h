#pragma once

#include "audiod/airplay-pipe.h"
#include "audiod/airplay-timing.h"
#include "audiod/child-log-reader.h"
#include "audiod/jailed-process.h"
#include "audiod/log.h"
#include "dbus/client.h"

#include <array>
#include <atomic>
#include <chrono>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

#ifndef CADENCE_MEDIA_AIRPLAY_LAN
#error "CADENCE_MEDIA_AIRPLAY_LAN must be defined as 0 or 1"
#endif

namespace cadence::audiod {

// avahi and nqptp face the lan (multicast dns; ptp on ports 319 and 320), which the sim has none of: only a sim build
// leaves them out (M4d-D8), and runs the bus and shairport alone. a device build always runs them, with no switch at
// run time
inline constexpr bool kAirplayLan = CADENCE_MEDIA_AIRPLAY_LAN != 0;

// what cadenced's AirplayConfig asks for (M4d-D7)
struct AirplaySettings {
  bool enabled = false;
  // the receiver's name on the lan; never logged
  std::string name;
  // the board's airplay dir: the runtime at <dir>/runtime
  std::string dir;
  // the daemons' scratch: their configs, the bus socket, the fifos
  std::string scratchDir;

  bool operator==(const AirplaySettings&) const = default;
};

// v1's schedule (reference §2.2, §2.5); tests shorten it
struct AirplayTimings {
  // the bus socket, avahi's name and nqptp's interface each come within it
  std::chrono::milliseconds resourceWait{2000};
  // shairport's name comes within it
  std::chrono::milliseconds receiverWait{10000};
  // a wait looks again this often, and nudges nqptp as often
  std::chrono::milliseconds tick{10};
  // restart waits: the first, doubling up to the last, and from the first again after a run of steady
  std::chrono::milliseconds firstBackoff{1000};
  std::chrono::milliseconds lastBackoff{60000};
  std::chrono::milliseconds steady{60000};
  // a stop's SIGTERM gives shairport and avahi this long before the SIGKILL, for their mdns goodbyes: no phone keeps
  // showing a receiver that went
  std::chrono::milliseconds stopGrace{500};
};

// where the daemons' fixed resources are; tests move them
struct AirplayOptions {
  AirplayTimings timings{};
  // avahi's own paths are compiled into it below this base (M4d-R5): made afresh per attempt, its etc/ left absent
  std::string avahiBase = "/tmp/cadence-airplay/avahi";
  // where nqptp's shm_open("/nqptp") lands
  std::string timingPath = "/dev/shm/nqptp";
  std::string interfaces = "/sys/class/net";
  airplay::TimingPorts ports{};
  // where the daemons' lines go (M6b-D7): none without one, their output then /dev/null
  ChildLogTarget logs{};
};

namespace airplay {

// every daemon runs through the runtime's own loader (M4d-R4): runtime/lib/<loader> --library-path runtime/lib
// runtime/bin/<program>
#if defined(__aarch64__)
inline constexpr std::string_view kLoader = "ld-linux-aarch64.so.1";
#elif defined(__x86_64__)
inline constexpr std::string_view kLoader = "ld-linux-x86-64.so.2";
#elif defined(__arm__) && defined(__ARM_PCS_VFP)
inline constexpr std::string_view kLoader = "ld-linux-armhf.so.3";
#else
#error "the airplay runtime is built for arm64, armhf and amd64 alone: configure with CADENCE_MEDIA_AIRPLAY=OFF"
#endif

// the loaders a runtime may bring, the first present taken. a 32-bit arm audiod on a 64-bit kernel (midi) runs the arm64
// runtime: an armhf glibc asks for the time64 calls (clock_gettime64) first on every clock read, and midi's 4.9 kernel
// predates them
#if defined(__arm__)
inline constexpr std::array<std::string_view, 2> kLoaders{kLoader, "ld-linux-aarch64.so.1"};
#else
inline constexpr std::array<std::string_view, 1> kLoaders{kLoader};
#endif

inline constexpr std::string_view kAvahiName = "org.freedesktop.Avahi";
inline constexpr std::string_view kShairportName = "org.gnome.ShairportSync";

// why an attempt failed, as AirplayStatus carries it
inline constexpr std::string_view kRuntimeMissing = "runtime_missing";
inline constexpr std::string_view kNoInterface = "no_interface";
inline constexpr std::string_view kScratchFailed = "scratch_failed";
inline constexpr std::string_view kInvalidName = "invalid_name";
inline constexpr std::string_view kForeignService = "foreign_service";
inline constexpr std::string_view kPortsBusy = "ports_busy";
inline constexpr std::string_view kSpawnFailed = "spawn_failed";
inline constexpr std::string_view kBusFailed = "bus_failed";
inline constexpr std::string_view kTimeout = "timeout";
inline constexpr std::string_view kExited = "exited";
// a stop, a disable, a new config or a shut gate came while it started: no failure
inline constexpr std::string_view kInterrupted = "interrupted";

}  // namespace airplay

// one attempt's daemons (reference §2.2, M4d-R4, M4d-R5): the receiver id, the scratch and avahi's base, the three
// configs, then dbus-daemon on a socket in the scratch, avahi-daemon on that bus, nqptp on its ports (bound first,
// never taken from a foreign holder) with a fresh interface, both fifos and their reader, and shairport-sync, each
// waited for before the next starts. each runs unjailed through the runtime's loader with a closed environment
// (PATH=/nonexistent, HOME and GIO_MODULE_DIR and GCONV_PATH in the scratch, GIO_USE_VFS=local, the bus address for
// avahi and shairport), stdin on /dev/null, stdout and stderr each a pipe a reader of the attempt's own drains into the
// options' logs as the daemon's lines (dbus, avahi, nqptp, shairport-sync; M6b-D7), or /dev/null without logs, nice 5
// and oom_score_adj 500 like soloist, and dies with audiod. glibc may still dlopen a host libnss_* module as the host's
// nsswitch.conf names it (accepted: pickle has none). a sim build without the lan daemons (kAirplayLan) neither checks
// nor runs avahi and nqptp, writes no avahi config, makes no avahi base and leaves nqptp's ports and interface alone.
// runs on the supervisor's thread, which it hands its waits to; logs carry daemon names, codes and pids, never the
// receiver's name or what a daemon printed
class AirplayStack {
 public:
  struct Hooks {
    // waits up to wait while the supervisor's thread watches its stop and command fds; false once the attempt is to
    // give up
    std::function<bool(std::chrono::milliseconds wait)> idle;
    // a daemon exited: called on its own thread
    std::function<void()> exited;
    // readable once the supervisor stops: a d-bus connect or send gives up at once
    int stopFd = -1;
    // what the metadata pipe says, each time it changed: called on the pipe's thread
    AirplayPipe::MetadataSink metadata{};
    // where the track stands, each time the pipe says: called on the pipe's thread
    AirplayPipe::ProgressSink progress{};
  };

  AirplayStack(const Log& log, AirplaySettings settings, const AirplayOptions& options, AirplayOutput& output,
               Hooks hooks);
  ~AirplayStack();

  AirplayStack(const AirplayStack&) = delete;
  AirplayStack& operator=(const AirplayStack&) = delete;

  // the sequence: empty once shairport owns its name, else why not (airplay::k…). whatever it started stays until stop
  std::string_view start();
  // in reverse order: shairport, the fifos' reader, nqptp, avahi, the bus; each killed and reaped, shairport and avahi
  // after a SIGTERM and the stop grace (their goodbyes: shairport's through avahi, which is still up then). those not
  // reaped within the reap wait are handed back, and the next attempt waits for them
  std::vector<std::unique_ptr<JailedProcess>> stop();

  // a daemon exited since it started
  [[nodiscard]] bool exited() const { return exited_->load(); }
  // the connection to the private bus and shairport's unique name there, once start succeeded
  [[nodiscard]] dbus::Client& client() { return *client_; }
  [[nodiscard]] const std::string& owner() const { return owner_; }
  // the bus still names owner() for shairport: asked once the supervisor's signal handler is in, so a change the
  // start dispatched before it is not missed
  [[nodiscard]] bool stillOwner();

 private:
  enum class Waited : std::uint8_t { Ready, Timeout, Exited, Interrupted };

  struct Daemon {
    std::string_view name;
    std::unique_ptr<JailedProcess> process;
  };

  std::string_view prepare();
  std::string_view startBus();
  std::string_view startAvahi();
  std::string_view startTiming();
  std::string_view startReceiver();
  std::string_view spawn(std::string_view name, std::string_view program, std::vector<std::string> args, bool bus);
  // polls ready every tick until it holds, within limit
  Waited waitFor(std::chrono::milliseconds limit, const std::function<bool()>& ready);
  // empty when ready, else its code, logged
  std::string_view failure(Waited waited) const;
  // the bus's answer for a name, nullopt when it did not answer
  std::optional<bool> hasOwner(std::string_view name);
  // shairport's unique name, nullopt while it has none or the bus did not answer
  std::optional<std::string> shairportOwner();

  const Log& log_;
  const AirplaySettings settings_;
  const AirplayOptions& options_;
  AirplayOutput& output_;
  const Hooks hooks_;
  // set on a daemon's own thread
  std::shared_ptr<std::atomic<bool>> exited_ = std::make_shared<std::atomic<bool>>(false);

  std::string runtime_;
  // runtime_/lib/<the loader prepare found>
  std::string loader_;
  std::uint64_t receiverId_ = 0;
  std::string address_;
  // in the order they started
  std::vector<Daemon> daemons_;
  std::optional<dbus::Client> client_;
  std::unique_ptr<AirplayPipe> pipe_;
  // the daemons' output, while the options have logs
  std::unique_ptr<ChildLogReader> lines_;
  std::string owner_;
};

}  // namespace cadence::audiod

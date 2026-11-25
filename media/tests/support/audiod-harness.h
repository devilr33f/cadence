#pragma once

#include "audiod/capture.h"
#include "audiod/child-logs.h"
#include "audiod/daemon.h"
#include "audiod/group-handlers.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-handlers.h"
#include "audiod/media-sessions.h"
#include "audiod/media-source.h"
#include "audiod/output.h"
#include "audiod/playback.h"
#include "audiod/soloist-handlers.h"
#include "audiod/tap-server.h"
#include "audiod/traces.h"
#include "support/eventually.h"

#ifdef CADENCE_MEDIA_AIRPLAY
#include "audiod/airplay-handlers.h"
#endif
#ifdef CADENCE_MEDIA_AIRPLAY_UXPLAY
#include "audiod/airplay-uxplay-handlers.h"
#endif

#include <catch2/catch_test_macros.hpp>

#include <stdlib.h>

#include <chrono>
#include <cstddef>
#include <filesystem>
#include <functional>
#include <future>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <system_error>
#include <thread>
#include <utility>
#include <vector>

namespace cadence::test_support {

// a private directory under the system temp dir, removed with its contents
class TempDir {
 public:
  TempDir() {
    auto pattern = (std::filesystem::temp_directory_path() / "cadence-audiod-XXXXXX").string();
    REQUIRE(::mkdtemp(pattern.data()) != nullptr);
    path_ = pattern;
  }
  ~TempDir() {
    std::error_code ignored;
    std::filesystem::remove_all(path_, ignored);
  }
  TempDir(const TempDir&) = delete;
  TempDir& operator=(const TempDir&) = delete;

  [[nodiscard]] std::string file(std::string_view name) const { return (path_ / name).string(); }

 private:
  std::filesystem::path path_;
};

// the lines a log wrote, readable from the test thread
class LogCapture {
 public:
  audiod::Log log() {
    return audiod::Log([this](std::string_view line) {
      std::scoped_lock lock(mutex_);
      lines_.emplace_back(line);
    });
  }

  [[nodiscard]] std::size_t count(std::string_view fragment) const {
    std::scoped_lock lock(mutex_);
    std::size_t matches = 0;
    for (const auto& line : lines_)
      if (line.find(fragment) != std::string::npos)
        ++matches;
    return matches;
  }

  [[nodiscard]] std::vector<std::string> lines() const {
    std::scoped_lock lock(mutex_);
    return lines_;
  }

  [[nodiscard]] bool eventually(std::string_view fragment) const {
    return test_support::eventually([&] { return count(fragment) > 0; });
  }

 private:
  mutable std::mutex mutex_;
  std::vector<std::string> lines_;
};

// what the media part of a harness is made with: by default every url is refused, as in a build without the url
// source
struct HarnessMedia {
  audiod::MediaSourceFactory factory = audiod::refuseUrls;
  audiod::WallClock clock = audiod::unixNowMs;
  std::chrono::milliseconds playBound = audiod::kUrlPlayBound;
  // when set, the factory is made from the harness's own loop and log instead, as main makes the url source's
  std::function<audiod::MediaSourceFactory(audiod::Loop& loop, const audiod::Log& log)> make{};
  // long-lived sources the sessions attach, as soloist's part attaches its own
  std::vector<audiod::LongLivedSource> sources{};
  // the rings MIXER external sessions are heard through; none by default, as in a build without CADENCE_MEDIA_AO_RING
  audiod::RingFactory rings = audiod::noRings;
  // the children's lines into the socket, as main wires them, in the soloist and airplay parts below
  bool childLogs = false;
  // when set, soloist's part too, as main adds it
  std::optional<audiod::SoloistOptions> soloist{};
  // the multiroom part too (GroupConfig, GroupStatus), its calibrations recording through the capture part, as main wires it
  bool group = false;
#ifdef CADENCE_MEDIA_AIRPLAY
  // when set, the airplay receiver's part too, as main adds it
  std::optional<audiod::AirplayOptions> airplay{};
  audiod::AirplayControlTimings airplayControl{};
#endif
#ifdef CADENCE_MEDIA_AIRPLAY_UXPLAY
  // when set, libairplay's receiver part too, as main adds it on a screen
  std::optional<audiod::uxplay::UxplayOptions> uxplay{};
#endif
};

// the daemon as main wires it, minus signals, with its loop on a background thread
class AudiodHarness {
 public:
  // an empty socket path picks one in the harness's own temp dir
  explicit AudiodHarness(std::vector<audiod::Backend> backends = audiod::compiledBackends(), std::string socket = {},
                         audiod::OutputFactory output = audiod::openOutput,
                         audiod::CaptureFactory capture = audiod::openCapture,
                         HarnessMedia media = {}, audiod::VendorOptions vendor = {},
                         audiod::VolumeRetry volumeRetry = {})
      : socket_(socket.empty() ? dir_.file("audiod.sock") : std::move(socket)),
        log_(logs_.log()),
        loop_(audiod::Loop::create(log_)) {
    REQUIRE(loop_);
    ipc_ = audiod::IpcServer::listen(*loop_, log_, socket_);
    REQUIRE(ipc_);
    childLogs_ = std::make_unique<audiod::ChildLogs>(*loop_, *ipc_, log_);
    traces_ = std::make_unique<audiod::Traces>(*loop_, *ipc_, log_);
    daemon_ = std::make_unique<audiod::Daemon>(*ipc_, log_, std::move(backends));
    auto playback = std::make_unique<audiod::Playback>(*loop_, *ipc_, log_, std::move(output), volumeRetry);
    playback->traceTo(*traces_);
    playback_ = playback.get();
    auto factory = media.make ? media.make(*loop_, log_) : std::move(media.factory);
    auto sessions = std::make_unique<audiod::MediaHandlers>(*loop_, *ipc_, log_, *playback, std::move(factory),
                                                            std::move(media.clock), media.playBound,
                                                            std::move(media.rings));
    media_ = sessions.get();
    for (const auto& make : media.sources)
      sessions->attach(make);
    auto* handlers = sessions.get();
    auto captured = std::make_unique<audiod::Capture>(*loop_, *ipc_, log_, std::move(capture), std::move(vendor));
    capture_ = captured.get();
    captured->traceTo(*traces_);
    std::unique_ptr<audiod::GroupHandlers> group;
    if (media.group) {
      group = std::make_unique<audiod::GroupHandlers>(*loop_, *ipc_, log_, *playback, *sessions);
      group->useMic([mic = capture_](std::int64_t from, std::int64_t to, std::function<void(sync::MicResult)> done) {
        mic->record(from, to, std::move(done));
      });
    }
    daemon_->add(std::move(playback));
    daemon_->add(std::move(captured));
    daemon_->add(std::move(sessions));
    auto tap = std::make_unique<audiod::TapServer>(*loop_, *ipc_, log_, *playback_);
    auto* tapServer = tap.get();
    if (group)
      tap->useGroupClock([g = group.get()](std::int64_t nowMono) { return g->heardClock(nowMono); });
    daemon_->add(std::move(tap));
    if (group)
      daemon_->add(std::move(group));
    if (media.soloist && media.childLogs)
      media.soloist->logs = childLogs_->target();
    if (media.soloist)
      daemon_->add(std::make_unique<audiod::SoloistHandlers>(*loop_, *ipc_, log_, *playback_, *handlers,
                                                             std::move(*media.soloist)));
#ifdef CADENCE_MEDIA_AIRPLAY
    if (media.airplay && media.childLogs)
      media.airplay->logs = childLogs_->target();
    if (media.airplay)
      daemon_->add(std::make_unique<audiod::AirplayHandlers>(*loop_, *ipc_, log_, *playback_, *handlers,
                                                             std::move(*media.airplay), media.airplayControl));
#endif
#ifdef CADENCE_MEDIA_AIRPLAY_UXPLAY
    if (media.uxplay) {
      auto uxplay = std::make_unique<audiod::uxplay::UxplayHandlers>(*loop_, *ipc_, log_, *playback_, *handlers,
                                                                     *tapServer, std::move(*media.uxplay));
      uxplay_ = uxplay.get();
      daemon_->add(std::move(uxplay));
    }
#endif
    (void)tapServer;
    thread_ = std::thread([this] { loop_->run(); });
  }

  ~AudiodHarness() {
    loop_->stop();
    thread_.join();
  }

  AudiodHarness(const AudiodHarness&) = delete;
  AudiodHarness& operator=(const AudiodHarness&) = delete;

  [[nodiscard]] const std::string& socket() const { return socket_; }
  // a path in the harness's own temp dir
  [[nodiscard]] std::string file(std::string_view name) const { return dir_.file(name); }
  [[nodiscard]] const LogCapture& logs() const { return logs_; }
  // the playback part, whose inputs a test feeds as a media source would
  [[nodiscard]] audiod::Playback& playback() const { return *playback_; }
  // the capture part, whose recordings a calibration takes (Capture::record is safe from any thread)
  [[nodiscard]] audiod::Capture& capture() const { return *capture_; }
  // the media part, whose external sources a test reaches on the loop thread
  [[nodiscard]] audiod::MediaHandlers& media() const { return *media_; }
  // for what a test posts as a source's own thread would
  [[nodiscard]] audiod::Loop& loop() const { return *loop_; }
  // where a test hands lines as a child's reader would
  [[nodiscard]] audiod::ChildLogTarget childLogs() const { return childLogs_->target(); }
#ifdef CADENCE_MEDIA_AIRPLAY_UXPLAY
  // the libairplay part, when the media asked for it
  [[nodiscard]] audiod::uxplay::UxplayHandlers* uxplay() const { return uxplay_; }
#endif

  // runs fn on the loop thread and waits for its result
  template <typename F>
  auto onLoop(F fn) {
    std::packaged_task<decltype(fn(*ipc_, *daemon_))()> task([&] { return fn(*ipc_, *daemon_); });
    auto result = task.get_future();
    loop_->post([&task] { task(); });
    return result.get();
  }

 private:
  TempDir dir_;
  std::string socket_;
  LogCapture logs_;
  audiod::Log log_;
  std::unique_ptr<audiod::Loop> loop_;
  std::unique_ptr<audiod::IpcServer> ipc_;
  // before the parts, which send through it until they stop
  std::unique_ptr<audiod::ChildLogs> childLogs_;
  // before the parts too, which mark through it until they stop
  std::unique_ptr<audiod::Traces> traces_;
  std::unique_ptr<audiod::Daemon> daemon_;
  audiod::Playback* playback_ = nullptr;
  audiod::MediaHandlers* media_ = nullptr;
  audiod::Capture* capture_ = nullptr;
#ifdef CADENCE_MEDIA_AIRPLAY_UXPLAY
  audiod::uxplay::UxplayHandlers* uxplay_ = nullptr;
#endif
  std::thread thread_;
};

}  // namespace cadence::test_support

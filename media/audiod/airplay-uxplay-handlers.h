#pragma once

#include "audiod/airplay-uxplay-mirror.h"
#include "audiod/airplay-uxplay-receiver.h"
#include "audiod/airplay-uxplay-session.h"
#include "audiod/daemon.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-handlers.h"
#include "audiod/playback.h"
#include "audiod/tap-server.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <chrono>
#include <cstdint>
#include <memory>
#include <optional>
#include <string>

namespace cadence::audiod::uxplay {

// how often the session is ticked while the receiver runs: what fell due is pushed, the clock anchored
inline constexpr std::chrono::milliseconds kTickEvery{10};
// how often a streaming receiver logs airplay_audio
inline constexpr std::chrono::seconds kAudioStatsEvery{10};

struct UxplayOptions {
  // the rtsp port; 0 = an ephemeral one (the tests)
  std::uint16_t port = 7000;
  SessionClocks clocks = systemClocks();
};

enum class ReceiverState : std::uint8_t { Off, Starting, Ready, Failed };

// libairplay's receiver over the socket (CADENCE_MEDIA_AIRPLAY=uxplay, the screens). AirplayConfig (backend libairplay
// or unspecified; shairport is refused as backend_unavailable) configures it: the receiver runs while it is enabled
// and the playback gate is open (rule 7), and is started again when what it advertises changes. its files live under
// the config's scratch_dir: the pairing key, the device id (6 random bytes made once), the paired senders and the
// cover (run/artwork, which cadenced uploads by the metadata's artwork id).
//
// out: AirplayStatus (the latest, at each AirplayConfig and on change), MirrorState as mirroring begins and ends,
// AirplayPin as a pin is shown and hidden, SenderVolume{airplay} for each deliberate slider move. SenderVolumeSet
// {airplay} is the speaker's level the sender's slider starts at. mirror frames go to the screen on the tap (TapConfig
// mirror on), the presentation clock anchored where the audio is. hls casts are advertised (a screen's receiver), and
// each one goes to cadenced as HlsCast (play with its url and start, scrub, rate, stop) under a session id of its own;
// cadenced's HlsPlaybackInfo for it is what the sender's /playback-info answers, and its end (ended) drops the sender.
// nothing a sender says is logged (rule 8)
class UxplayHandlers final : public Part {
 public:
  UxplayHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media, TapServer& tap,
                 UxplayOptions options = {});
  ~UxplayHandlers() override;

  UxplayHandlers(const UxplayHandlers&) = delete;
  UxplayHandlers& operator=(const UxplayHandlers&) = delete;

  void configure(const Configure& configuration) override;
  void reset() override;

  // the running receiver's port, 0 when none: for the tests, on the loop thread
  [[nodiscard]] std::uint16_t port() const { return receiver_ ? receiver_->port() : 0; }
  [[nodiscard]] Receiver* receiver() const { return receiver_.get(); }
  [[nodiscard]] const MirrorForwarder& forwarder() const { return forwarder_; }
  // what libairplay's threads call: the tests drive it as a sender would
  [[nodiscard]] Session& session() const { return *session_; }

 private:
  struct Settings {
    bool enabled = false;
    ReceiverSettings receiver;
    SessionSettings session;
    bool operator==(const Settings&) const;
  };

  void config(const cadence_audiod_v1_AirplayConfig& request);
  void apply();
  void startReceiver();
  void stopReceiver(std::string_view reason);
  void arm();
  void tick();
  void status(ReceiverState state, std::string_view code = {});
  void flushStatus();
  void mirrorState(const MirrorReport& report);
  void pin(const PinReport& report);
  void senderVolume(std::uint8_t level);
  void hls(const HlsRequest& request);
  void playbackInfo(const cadence_audiod_v1_HlsPlaybackInfo& info);
  [[nodiscard]] std::optional<std::array<std::uint8_t, 6>> deviceId(const std::string& scratch) const;

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  Playback& playback_;
  TapServer& tap_;
  UxplayOptions options_;
  Settings settings_;
  bool gateOpen_ = false;
  // this connection sent its config
  bool announced_ = false;
  bool statusPending_ = false;
  ReceiverState state_ = ReceiverState::Off;
  std::string code_;
  std::optional<Loop::TimerId> timer_;
  std::optional<Loop::TimerId> resend_;
  MirrorForwarder forwarder_;
  bool anchored_ = false;
  // the last airplay_audio line, and whether a window since had audio
  std::chrono::steady_clock::time_point statsAt_{};
  void logAudioStats();
  // the hls cast cadenced plays now ("" for none), and how many were made
  std::string cast_;
  std::uint64_t casts_ = 0;
  // the session outlives the receiver, whose threads call it; the media sessions' source keeps it too
  std::shared_ptr<Session> session_;
  std::unique_ptr<Receiver> receiver_;
};

}  // namespace cadence::audiod::uxplay

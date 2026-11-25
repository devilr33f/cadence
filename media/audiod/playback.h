#pragma once

#include "audio/opus-codec.h"
#include "audiod/daemon.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/output.h"
#include "audiod/prompts.h"
#include "audiod/ring-sequence.h"
#include "audiod/stream-recorder.h"
#include "audiod/system-volume.h"
#include "audiod/traces.h"
#include "backend/pcm-backend.h"
#include "backend/pcm-input.h"
#include "backend/span-histogram.h"
#include "dsp/duck.h"
#include "dsp/eq.h"
#include "dsp/viz.h"
#include "playback/alsa-player.h"

#include <atomic>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace cadence::audiod {

// the device side of the playback over a window: counts and ms, never audio
struct OutputWindow {
  bool aaudio = false;
  // the player's recoveries (a device underrun it restarted after), and the aaudio ring's underruns and aaudio's own
  // xruns over the window
  std::uint64_t recoveries = 0;
  std::uint64_t underruns = 0;
  std::int64_t xruns = 0;
  // aaudio: its burst and the ring's fill target in frames, the least the ring held at a callback, and the time
  // between callbacks
  std::int32_t burstFrames = 0;
  std::size_t fillFrames = 0;
  std::size_t fillMinFrames = 0;
  SpanSummary period;
  // the playback thread's nice and the errno of its raise
  ThreadPriority priority;
};

// when a hardware volume the control refused or did not read back is set again: min after the failure, doubling to
// max between the attempts after it (ruling M2-R10)
struct VolumeRetry {
  std::chrono::milliseconds min = std::chrono::seconds(1);
  std::chrono::milliseconds max = std::chrono::seconds(30);
};

// the device's speaker. StreamStart/AudioDown/StreamEnd are decoded into the mixer's voice source, and each
// stream gets one StreamDone{played_ms}: once its last sample played, or when it is cut (a newer stream
// replaced it while it still arrived, a StreamCancel of it or of a stream queued before it, a new Configure, a
// failed volume), with what was read of it by then.
// Tone plays an earcon on the tone source over the voice. ToneStart loops an alarm's or a timer's ring there too,
// gapless, one at a time, until the ToneStop of its id, and ToneState answers it: sounding, or gated, busy (another
// rings) or invalid; stopped once its ToneStop took it back from the device, and gated when the gate shut under it. the
// id that sounds, asked for again, sounds on, and with the other kind it loops that kind from then on. a ToneStart with
// segments rings them in order (RingSequence): a tone kind's sound, a prompt, silence, or a url streamed through
// useRingStreams' factory with the request's Authorization header; a url that fails or stays silent for 3 s switches
// the ring to its fallback, which a ToneState{sounding, fallback, code} and a ring_fallback line tell.
// streams still play over a ring. VolumeSet sets the board's hardware control, or a software gain after the backend's
// limiter without one, and is answered by VolumeApplied, which says a transient set back and why a level failed.
// the music and overlay inputs play through the player's pcm backend; DuckSet ducks the music, MediaSettings set
// the duck mode, the user eq and the viz, whose frames go out as VizFrame while it is on. Configure's output gives
// the channels and the board's eq profile. the settings outlast a new Configure; cadenced going away resets them.
// StreamStart.duck is not read: every voice and tone sample ducks the music, as in v1.
//
// rule 7: only VolumeSet moves the volume, and nothing plays until one held. Configure's volume section is
// never applied: cadenced sends the persisted level as its first VolumeSet. before that a stream is refused at
// once with StreamDone{played_ms: 0}, a tone is dropped and the music inputs are read away unheard; a VolumeSet
// that fails closes the gate again, and so do a new Configure and cadenced going away. a level the hardware control
// refused or did not read back is set again after VolumeRetry's backoff, the gate shut meanwhile, until it holds,
// which a second VolumeApplied{ok} tells, or until a newer VolumeSet, a new Configure or cadenced going away ends the
// retries: a retry sets only the level of the last VolumeSet that was not transient. a transient one (an alarm's
// ramp) is set and read back the same way, but only while the gate is open and a ring sounds: it never opens or shuts
// the gate, is never set again, and a failure only logs. every call runs on the loop thread; the player's completions
// and viz frames come back through Loop::post
class Playback final : public Part {
 public:
  Playback(Loop& loop, IpcServer& ipc, const Log& log, OutputFactory open = openOutput, VolumeRetry retry = {});
  ~Playback() override;
  Playback(const Playback&) = delete;
  Playback& operator=(const Playback&) = delete;

  // the streams' trace marks (traces.h): PLAYBACK_OPENED once a stream entered the mix, FIRST_SAMPLE_PLAYED and
  // UNDERRUN from the playback thread (AlsaPlayer::traceTo), STREAM_DONE{played_ms, underruns} just before its
  // StreamDone. none until set; set before the loop runs, with traces outliving this part
  void traceTo(Traces& traces) { traces_ = &traces; }

  void configure(const Configure& configuration) override;
  // the system volume as it stood at Configure, where the output's level is the system's
  void fillReady(cadence_audiod_v1_Ready& ready) override;
  void reset() override;

  // what the media session and the url overlay push into; they outlive every player. one producer thread at a time
  // (see PcmInput): a new producer starts only once the old one was seen done with its last push (a url source's stop
  // returns when no push is under way), and a producer that stops calls clear(), since a push waits while no player
  // reads (no configuration, or a closed one)
  [[nodiscard]] PcmInput& music() { return music_; }
  [[nodiscard]] PcmInput& overlay() { return overlay_; }
  // where the session's player ring goes while it holds the speaker (an ExternalSource on the MIXER route)
  [[nodiscard]] PcmReaderSlot& external() { return external_; }

  // SessionControl for the whole mix. the server's duck lowers the music to level of itself (never raises it, rule
  // 7); a level is a VolumeSet's, answered by VolumeApplied as one; the mute silences the mix after the limiter; an eq
  // replaces MediaSettings' user eq. like those settings they outlast a new Configure, and cadenced going away resets
  // them
  void serverDuck(bool active, float level, std::uint32_t rampMs);
  void setLevel(std::uint32_t level) { setVolume(level); }
  void setMuted(bool muted);
  // the board's audio delay trim, ms (see PcmBackend::setDelayTrim): Configure's OutputConfig.delay_ms, then
  // OutputDelay's
  void setDelayTrim(std::int32_t ms);
  // frames the output reads at a time, which a ring is sized from; 0 before an output opened
  [[nodiscard]] std::size_t burstFrames() const { return burstFrames_; }
  // how a ring's url segments are streamed; none (the default) fails each at once as unsupported, so its fallback
  // sounds. set before the loop runs
  void useRingStreams(RingStreamFactory streams) { ringStreams_ = std::move(streams); }
  void setUserEq(const cadence_audiod_v1_EqSettings& eq);
  // the screen's tap: pcm snapshots of the mix while on; none without a player
  void setSnapshots(bool enabled);
  [[nodiscard]] bool snapshot(PcmSnapshot& out) const;
  // each runs with every viz frame the player made while the viz is on, beside the VizFrame cadenced gets; register
  // before the loop runs
  void onViz(std::function<void(const dsp::VizFrame& frame)> hook) { vizHooks_.push_back(std::move(hook)); }
  // the mixer's presentation clock, none without a player
  [[nodiscard]] PresentationClock* clock();
  // the output since the last call, for a source's stats line (airplay_audio); the loop's, none without a player
  [[nodiscard]] std::optional<OutputWindow> takeOutputWindow();

  // whether a VolumeSet held since the gate last shut
  [[nodiscard]] bool gateOpen() const { return gateOpen_; }
  // the mix's duck (a voice's or the server's) and mute as last applied, for the logs
  [[nodiscard]] bool ducked() const { return controls_.ducked || controls_.serverDucked || groupDucked_; }
  // each runs with every DuckSet (this device's own duck, a wake), which a group sends its leader; register before the
  // loop runs
  void onDuck(std::function<void(bool active)> hook) { duckHooks_.push_back(std::move(hook)); }
  // the group's duck (any device's wake), or-ed with this device's own
  void setGroupDuck(bool active);
  [[nodiscard]] bool muted() const { return controls_.muted; }
  // each runs when a failed VolumeSet shuts the gate; a Configure and cadenced going away shut it too, which every part
  // sees itself. register before the loop runs
  void onGateShut(Loop::Callback hook) { gateShut_.push_back(std::move(hook)); }
  // each runs when a VolumeSet opens the gate that was shut
  void onGateOpen(Loop::Callback hook) { gateOpened_.push_back(std::move(hook)); }

 private:
  struct Stream {
    std::string id;
    // samples the voice source took
    std::size_t accepted = 0;
    bool ended = false;
    std::optional<StreamRecorder> recorder;
    // the voice queue's underruns when it began, so its STREAM_DONE counts its own
    std::uint64_t underruns = 0;
  };

  // what MediaSettings and DuckSet set, applied to each new player
  struct MediaControls {
    bool ducked = false;
    dsp::DuckMode duckMode = dsp::DuckMode::Duck;
    dsp::EqSettings userEq;
    bool vizEnabled = false;
    VizSource vizSource = VizSource::Music;
    bool serverDucked = false;
    float serverDuckLevel = 1.0f;
    std::uint32_t serverDuckRampMs = 0;
    bool muted = false;
    bool snapshots = false;
    std::int32_t delayTrimMs = 0;
  };

  void start(const cadence_audiod_v1_StreamStart& request);
  void audio(const cadence_audiod_v1_AudioDown& packet);
  void end(const cadence_audiod_v1_StreamEnd& request);
  void cancelStream(const cadence_audiod_v1_StreamCancel& request);
  // why a level did not hold, and whether setting it again may help
  struct VolumeFailure {
    std::string_view reason;
    bool retry = false;
  };

  void setVolume(std::uint32_t level);
  void setTransientVolume(std::uint32_t level);
  void retryVolume(std::uint32_t level);
  // sets level on the control, or as the software gain without one
  [[nodiscard]] std::optional<VolumeFailure> applyVolume(std::uint32_t level);
  void volumeHeld(std::uint32_t level);
  void volumeFailed();
  void armVolumeRetry(std::uint32_t level);
  void cancelVolumeRetry();
  void tone(const cadence_audiod_v1_Tone& request);
  void startRing(const cadence_audiod_v1_ToneStart& request);
  void stopRing(std::string_view id);
  // the ring that sounds goes quiet, with a ToneState carrying code when report is set
  void silenceRing(std::string_view code, bool report);
  // an alarm's or a timer's period, or the reminder's chime; null for any other kind
  [[nodiscard]] const std::vector<std::int16_t>* ringPeriod(cadence_audiod_v1_ToneKind kind) const;
  // a ToneStart's segments as the ring's sequence; null when one is invalid (none or several of its fields, an
  // unknown kind, a url in the fallback)
  [[nodiscard]] std::shared_ptr<RingSequence> ringSequence(const cadence_audiod_v1_ToneStart& request,
                                                           const std::vector<std::int16_t>& period);
  [[nodiscard]] std::optional<RingPart> ringPart(const cadence_audiod_v1_RingSegment& segment, bool streams,
                                                 const std::string& authorization);
  // the ring of serial left its url for its fallback, as the playback thread posted
  void ringFellBack(std::uint64_t serial, const std::string& code);
  void duck(bool active);
  void settings(const cadence_audiod_v1_MediaSettings& request);

  // hands the controls to the player's backend
  void applyControls();
  // cuts every stream, reporting each when report is set, stops the player and closes the gate
  void close(bool report);
  // the streams from index from on stop now and report what was read of them; the ones before play on
  void cut(bool report, std::size_t from = 0);
  void settled(std::uint64_t epoch, const std::string& key, bool success);
  // arms the player's one completion watch for the oldest stream still waiting for it
  void armNext();
  void finish(Stream& stream, std::size_t playedSamples, bool report);
  // traceId: the StreamStart's trace, logged beside the reason when it carried one
  void refuse(std::string_view id, std::string_view reason, std::string_view traceId = {});
  // underruns: the stream's own, when it got as far as the mix
  void sendDone(std::string_view id, std::size_t playedSamples, std::uint64_t underruns = 0);
  [[nodiscard]] std::uint64_t voiceUnderruns() const;
  void sendApplied(std::uint32_t level, bool ok, bool transient = false, std::string_view code = {});
  void sendToneState(std::string_view id, bool sounding, std::string_view code, bool fallback = false);
  void sendViz(std::uint64_t epoch, const dsp::VizFrame& frame);
  // the host said the system volume changed: a move of its own goes to cadenced as SystemVolume
  void systemChanged(std::uint64_t epoch);
  [[nodiscard]] std::deque<Stream>::iterator find(std::string_view id);

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  Traces* traces_ = nullptr;
  OutputFactory open_;
  VolumeRetry retry_;
  OpusDecoder decoder_;
  const std::vector<std::int16_t> wakeTone_;
  const std::vector<std::int16_t> errorTone_;
  // the timer's ring period, which is the done earcon too: two short beeps
  const std::vector<std::int16_t> timerRing_;
  const std::vector<std::int16_t> alarmRing_;
  const std::vector<std::int16_t> reminderChime_;
  RingStreamFactory ringStreams_;
  // Configure's ca bundle, which a ring's url verifies against
  std::string caFile_;
  // the spoken prompts a Tone may name, under Configure.prompts_dir
  Prompts prompts_;

  PcmInput music_;
  PcmInput overlay_;
  PcmReaderSlot external_;
  std::size_t burstFrames_ = 0;
  MediaControls controls_;
  // the configuration's board eq
  dsp::EqSettings profile_;
  // a viz frame is on its way to the loop; the playback thread drops the next ones until it is sent
  std::atomic<bool> vizPending_{false};

  // the output the configuration opened; the sink outlives the player that writes it
  std::unique_ptr<PcmSink> sink_;
  std::unique_ptr<VolumeControl> volume_;
  // volume_ when it is the host's system volume, else null
  SystemVolumeControl* system_ = nullptr;
  std::optional<std::string> recordDir_;
  std::unique_ptr<AlsaPlayer> player_;
  // takeOutputWindow's last counters
  std::uint64_t seenRecoveries_ = 0;
  // aaudio's alone: unused on the other backends
  [[maybe_unused]] std::uint64_t seenUnderruns_ = 0;
  [[maybe_unused]] std::int64_t seenXruns_ = 0;
  // counts players, so a completion a stopped one posted is told apart
  std::uint64_t epoch_ = 0;
  bool gateOpen_ = false;
  // the level the last held or transient apply set, so the re-check before each reply logs only a change
  std::optional<std::uint32_t> lastApplied_;
  // the retry of the last VolumeSet's level while one is armed, the wait before the next and how many ran
  std::optional<Loop::TimerId> volumeRetry_;
  std::chrono::milliseconds retryDelay_{0};
  std::uint64_t retries_ = 0;
  std::vector<Loop::Callback> gateShut_;
  std::vector<Loop::Callback> gateOpened_;
  std::vector<std::function<void(bool active)>> duckHooks_;
  bool groupDucked_ = false;
  std::vector<std::function<void(const dsp::VizFrame& frame)>> vizHooks_;
  // in the order they began, which is the order they play
  std::deque<Stream> streams_;
  // the key the player watches, until its settlement arrives
  std::optional<std::string> watched_;
  // the ToneStart id of the ring that sounds, and the period it loops
  std::optional<std::string> ringing_;
  const std::vector<std::int16_t>* ringPeriod_ = nullptr;
  // the ring that sounds is a sequence of segments; serial counts rings, so a late fallback of an old one is told apart
  bool ringSequence_ = false;
  std::uint64_t ringSerial_ = 0;
  std::uint64_t tones_ = 0;
  std::uint64_t decodeErrors_ = 0;
  std::uint64_t overflows_ = 0;
};

}  // namespace cadence::audiod

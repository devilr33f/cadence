#pragma once

#include "backend/pcm-input.h"
#include "result.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <initializer_list>
#include <memory>
#include <string>
#include <string_view>

namespace cadence::audiod {

// video, screencast and external are a screen's player, airplay mirroring and another app or device: sessions
// audiod does not decode, which an ExternalSource stands in for. group is a multiroom group's audio from its leader
// (a member's GROUP session). bluetooth is a phone's a2dp pcm, which cadenced hands over as an fd
enum class MediaKind : std::uint8_t { Url, Soloist, Airplay, Stream, Video, Screencast, External, Group, Bluetooth };

// who renders a session's sound (cadence.audiod.v1.AudioRoute)
enum class AudioRoute : std::uint8_t { Mixer, Direct, External, Remote, None };

enum class AudioControl : std::uint8_t { Volume, Duck, Mute, Eq, VizPcm, VizBands };

class AudioControls {
 public:
  constexpr AudioControls() = default;
  constexpr AudioControls(std::initializer_list<AudioControl> controls) {
    for (const auto control : controls)
      bits_ = static_cast<std::uint8_t>(bits_ | bit(control));
  }

  [[nodiscard]] constexpr bool has(AudioControl control) const { return (bits_ & bit(control)) != 0; }
  constexpr void add(AudioControl control) { bits_ = static_cast<std::uint8_t>(bits_ | bit(control)); }
  constexpr bool operator==(const AudioControls&) const = default;

 private:
  static constexpr std::uint8_t bit(AudioControl control) {
    return static_cast<std::uint8_t>(1U << static_cast<unsigned>(control));
  }

  std::uint8_t bits_ = 0;
};

// every control, in the order MediaState lists them
inline constexpr AudioControl kAudioControls[] = {AudioControl::Volume, AudioControl::Duck,   AudioControl::Mute,
                                                  AudioControl::Eq,     AudioControl::VizPcm, AudioControl::VizBands};

// what audiod's own sources take: they mix, so every control applies sample-exact
inline constexpr AudioControls kMixerControls{AudioControl::Volume, AudioControl::Duck,   AudioControl::Mute,
                                              AudioControl::Eq,     AudioControl::VizPcm, AudioControl::VizBands};

enum class MediaAction : std::uint8_t { Pause, Resume, Stop, Seek, Next, Previous };

// every action, in the order MediaState lists them
inline constexpr MediaAction kMediaActions[] = {MediaAction::Pause, MediaAction::Resume, MediaAction::Stop,
                                                MediaAction::Seek,  MediaAction::Next,   MediaAction::Previous};

class MediaActions {
 public:
  constexpr MediaActions() = default;
  constexpr MediaActions(std::initializer_list<MediaAction> actions) {
    for (const auto action : actions)
      bits_ = static_cast<std::uint8_t>(bits_ | bit(action));
  }

  [[nodiscard]] constexpr bool has(MediaAction action) const { return (bits_ & bit(action)) != 0; }
  constexpr bool operator==(const MediaActions&) const = default;

 private:
  static constexpr std::uint8_t bit(MediaAction action) {
    return static_cast<std::uint8_t>(1U << static_cast<unsigned>(action));
  }

  std::uint8_t bits_ = 0;
};

// memory only: never logged (rule 8)
struct MediaMetadata {
  std::string title;
  std::string artist;
  std::string album;
  std::string artworkUrl;
  // a cover handed over as bytes (airplay), written to the source's scratch: changes whenever the image does
  std::string artworkId;
  // the sending device's name (an airplay sender's), empty when unknown: memory-only, never logged (D11)
  std::string sender;

  bool operator==(const MediaMetadata&) const = default;
};

// MediaState's bounds in bytes (the nanopb sizes less the terminator); longer text is cut on a utf-8 boundary
inline constexpr std::size_t kMetadataTextBytes = 255;
inline constexpr std::size_t kArtworkUrlBytes = 2047;
inline constexpr std::size_t kArtworkIdBytes = 16;

// the codes of commands and failures: a fixed token, empty when ok
inline constexpr std::string_view kMediaOk;
inline constexpr std::string_view kMediaNoSession = "no_session";
inline constexpr std::string_view kMediaUnsupported = "unsupported";
inline constexpr std::string_view kMediaInvalid = "invalid";
inline constexpr std::string_view kMediaRefused = "refused";
inline constexpr std::string_view kMediaFailed = "failed";
// a url that had not played yet when a newer one or a stop took its place
inline constexpr std::string_view kMediaReplaced = "replaced";
inline constexpr std::string_view kMediaStopped = "stopped";
// a command whose code comes later, through the done it was made with (see MediaSource::command); never sent
inline constexpr std::string_view kMediaPending = "pending";

// a command's code when it comes later
using CommandDone = std::function<void(std::string_view code)>;

// what a source reports, always on the loop thread. it may report from inside a call the sessions make into it;
// they act on it once that call returned.
//
// a source holds the handle the factory gave it and reports only through it. its own threads post a copy of the
// handle with each report (Loop::post([events = events_] { events->playing(); })), never the source itself: a
// report still queued when the source is destroyed then reaches the sessions, which drop it
class MediaSourceEvents {
 public:
  virtual ~MediaSourceEvents() = default;

  virtual void playing() = 0;
  virtual void paused() = 0;
  virtual void buffering() = 0;
  // the end of what it plays, once what it queued has been read (PcmInputStatus::queued at 0, waited for with a
  // bound): the session ends with it and leaves the input alone
  virtual void ended() = 0;
  // code is a fixed token, never upstream text
  virtual void failed(std::string_view code) = 0;
  // the whole of it each time
  virtual void metadata(const MediaMetadata& metadata) = 0;
  // 0 = live or unknown
  virtual void duration(std::uint64_t ms) = 0;
  // a discontinuity only (start, seek, resume): between them the position runs with the playing state
  virtual void position(std::uint64_t ms) = 0;
};

// one producer of the session's audio, driven on the loop thread. every command returns a code (kMediaOk, or a
// fixed token such as unsupported or failed, which outlives the source); its effect comes back as events.
// destroying a source stops it: from then on it reports and pushes nothing. it never blocks the loop on a thread that
// may not answer (a url source lets its decoder finish on its own).
//
// the music input has one producer at a time (see PcmInput): a source pushes into it only after openOutput, and
// stops before closeOutput, pause or stop returns (a push that waits on a full input is released by clearing it,
// which the source does itself), so the next source's producer never overlaps it
class MediaSource {
 public:
  virtual ~MediaSource() = default;

  [[nodiscard]] virtual MediaKind kind() const = 0;
  // what it supports now
  [[nodiscard]] virtual MediaActions actions() const = 0;
  // whether it can wait paused while another source plays or the gate is shut (soloist, airplay); one that cannot,
  // like a url, which would hold neither its connection nor its position, is stopped and destroyed instead
  [[nodiscard]] virtual bool keepsPaused() const = 0;

  // the source took the session: its audio goes to music from now on
  virtual void openOutput(PcmInput& music) = 0;
  // it lost the session but waits paused: until the next openOutput it pushes nothing, whatever it reports
  virtual void closeOutput() = 0;

  virtual std::string_view pause() = 0;
  virtual std::string_view resume() = 0;
  virtual std::string_view stop() = 0;
  virtual std::string_view seek(std::uint64_t ms) = 0;
  virtual std::string_view next() = 0;
  virtual std::string_view previous() = 0;

  // the session id it takes the session under: empty for audiod's own sources, which get "ms-<n>"; an external source
  // keeps cadenced's id, so cadenced and the server know it by the id they gave it
  [[nodiscard]] virtual std::string_view sessionId() const { return {}; }
  [[nodiscard]] virtual AudioRoute audioRoute() const { return AudioRoute::Mixer; }
  [[nodiscard]] virtual AudioControls audioControls() const { return kMixerControls; }
  // why the next pause or stop the sessions make comes (displaced, gated), told just before it; nothing else
  // acts on it
  virtual void setAsideBy(std::string_view /*reason*/) {}

  // a command cadenced sent: the matching call above, whose code it returns. a source whose peer answers its controls
  // (soloist) returns kMediaPending instead and calls done once with the code later, on the loop thread, unless it is
  // destroyed first
  virtual std::string_view command(MediaAction action, std::uint64_t positionMs, CommandDone done);
};

// a long-lived source (soloist, airplay), made once with its events handle
using LongLivedSource = std::function<std::unique_ptr<MediaSource>(std::shared_ptr<MediaSourceEvents> events)>;

// what UrlPlay asks for; the url and the metadata are never logged
struct UrlRequest {
  std::string url;
  MediaMetadata metadata;
  std::uint64_t startMs = 0;
  // Configure's ca bundle, which https verifies against
  std::string caFile;
  // names the source in logs, the same on its url_* lines and on its session's start: never the url
  std::string logId{};
};

// makes the source of a url session, which reports through events; the error is a fixed token cadenced gets back
// as the command's code
using MediaSourceFactory = std::function<Result<std::unique_ptr<MediaSource>, std::string_view>(
    const UrlRequest& request, std::shared_ptr<MediaSourceEvents> events)>;

// a build without a url source refuses every url
Result<std::unique_ptr<MediaSource>, std::string_view> refuseUrls(const UrlRequest& request,
                                                                  std::shared_ptr<MediaSourceEvents> events);

}  // namespace cadence::audiod

#pragma once

#include "audiod/log.h"
#include "audiod/media-source.h"
#include "backend/pcm-input.h"

#include <cstddef>
#include <cstdint>
#include <deque>
#include <functional>
#include <list>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

enum class PlayState : std::uint8_t { Idle, Playing, Paused, Buffering };

// the session as MediaState carries it; an empty id is no session, and then the rest is idle and empty
struct MediaSession {
  std::string id;
  MediaKind kind = MediaKind::Url;
  PlayState state = PlayState::Idle;
  std::uint64_t positionMs = 0;
  // unix ms when positionMs was taken; it runs on from there while playing
  std::int64_t positionAtMs = 0;
  // 0 = live or unknown
  std::uint64_t durationMs = 0;
  MediaMetadata metadata;
  MediaActions actions;
  AudioRoute route = AudioRoute::Mixer;
  AudioControls controls = kMixerControls;
  // cadenced's ExternalSource id when an external source holds the session, else empty
  std::string externalId;

  [[nodiscard]] float rate() const { return state == PlayState::Playing ? 1.0f : 0.0f; }
};

// unix ms on the device clock
using WallClock = std::function<std::int64_t()>;
std::int64_t unixNowMs();

// the most sources that wait paused while another holds the session (an attached one among them): past it, the one set
// aside longest ago is stopped and destroyed, never an attached one
inline constexpr std::size_t kMaxKeptSources = 4;

// the one exclusive media session (M4b-D8), on the loop thread. a url request makes a source that waits until it
// reports playing; the last source to report playing takes the session (a new id "ms-<n>"): the one that held it is
// paused, or stopped and destroyed when it cannot wait paused, its output closed and the music input cleared before
// the winner's output opens. nothing ever resumes a displaced source: one that reports playing on its own takes the
// session back the same way. a session that ends, fails or is stopped leaves none; a stop cuts the music its source
// queued, an end or failure lets it play out.
//
// the gate (rule 7) shuts on a failed volume, a new Configure and cadenced going away: the session's source is paused
// with its output closed, or stopped when it cannot wait, and nothing resumes when the gate opens again. while it is
// shut a url is refused, so is a resume, and a source that reports playing is paused again.
//
// a long-lived source (soloist) is attached once and kept until the sessions go: it never has a request, takes the
// session when it reports playing like any other, and when its session ends, fails or is stopped it stays, idle, its
// output closed and its state forgotten. at most kMaxKeptSources wait paused.
//
// publish gets the session whenever its state, metadata, duration, actions or id changes, and at each position
// discontinuity: never on a timer
class MediaSessions {
 public:
  using Publish = std::function<void(const MediaSession& session)>;
  // a url request's answer: kMediaOk or a code
  using Done = std::function<void(std::string_view code)>;

  MediaSessions(const Log& log, PcmInput& music, std::function<bool()> gateOpen, MediaSourceFactory factory,
                Publish publish, WallClock now = unixNowMs);
  ~MediaSessions();

  MediaSessions(const MediaSessions&) = delete;
  MediaSessions& operator=(const MediaSessions&) = delete;

  // a url session. a refusal comes back at once (refused while the gate is shut, or the factory's code) and done is
  // dropped. kMediaOk: the source was made, replacing a request still waiting to play, and done is called once,
  // later: kMediaOk when it takes the session, its code when it fails first (failed when it ends first), replaced,
  // stopped or refused when a newer request, a stop or the gate drops it; never once the sessions are destroyed
  std::string_view play(const UrlRequest& request, Done done);
  // made now with its events handle, before any session
  void attach(const LongLivedSource& make);
  // a source made now that waits, idle, until it reports playing, like a url that was asked for but without a
  // request to answer (an ExternalSource); its serial, for remove
  std::uint64_t add(const LongLivedSource& make);
  // destroys what add made, without a stop: it is gone already. its session ends, cutting what it queued
  void remove(std::uint64_t serial);
  // the source of the session, if any
  [[nodiscard]] MediaSource* ownerSource();
  // the input the sources push into from now on (a group leader's feed, or the music input again): the session's
  // source closes its output on the old one, which is cleared, and opens it on the new. logged as group_source with
  // the session's kind (none without one) and the target (feed|music)
  void retarget(PcmInput& music);
  // the session's source does it; a stop that succeeds, or finds no session, also drops a request still waiting.
  // kMediaPending: the source answers later, through later (see MediaSource::command)
  std::string_view command(MediaAction action, std::uint64_t positionMs, CommandDone later = {});
  // the gate shut
  void gateShut();

  [[nodiscard]] const MediaSession& session() const { return published_; }

 private:
  class Events;
  struct Entry {
    std::uint64_t serial = 0;
    // shared with the source, so a report it posted reaches the serial check after the entry is gone
    std::shared_ptr<Events> events;
    std::unique_ptr<MediaSource> source;
    // what its logs call it
    std::string logId;
    // long-lived: never destroyed while the sessions last
    bool attached = false;
    // a url request that has not played yet, and its answer
    bool requested = false;
    Done done;
    bool outputOpen = false;
    PlayState state = PlayState::Idle;
    std::uint64_t positionMs = 0;
    std::int64_t positionAtMs = 0;
    std::uint64_t durationMs = 0;
    MediaMetadata metadata;
    // when it was last set aside, to tell the oldest of those that wait paused
    std::uint64_t setAside = 0;
  };
  enum class EventKind : std::uint8_t { Playing, Paused, Buffering, Ended, Failed, Metadata, Duration, Position };
  struct Event {
    std::uint64_t serial = 0;
    EventKind kind = EventKind::Playing;
    std::uint64_t value = 0;
    std::string code;
    MediaMetadata metadata;
  };
  // every entry point runs inside one, so an event a source reports from inside a call is handled after it
  class Busy;

  void report(Event event);
  void handle(const Event& event);
  void playing(Entry& entry);
  void moveTo(Entry& entry, PlayState state);
  // the session ends and its source is destroyed; what the source queued is cut, or plays out when it ended or
  // failed on its own
  void end(std::string_view reason, bool cut);
  // a source that lost or never had the session; one that cannot wait paused is stopped and destroyed
  void setAside(Entry& entry);
  // past kMaxKeptSources waiting paused, the oldest that is not attached goes
  void trimKept();
  // destroys the source, or forgets the state of an attached one, its output closed
  void destroy(std::uint64_t serial);
  // answers a request once
  static void answer(Entry& entry, std::string_view code);
  void openOutput(Entry& entry);
  void closeOutput(Entry& entry);
  void dropRequested(std::string_view code);
  void publish(bool discontinuity);
  [[nodiscard]] Entry* find(std::uint64_t serial);
  [[nodiscard]] Entry* owner();
  [[nodiscard]] MediaSession snapshot();

  const Log& log_;
  PcmInput* music_;
  // the device's own music input, which retarget's log tells from a group leader's feed
  PcmInput* const home_;
  std::function<bool()> gateOpen_;
  MediaSourceFactory factory_;
  Publish publish_;
  WallClock now_;

  // a list, so an entry stays put while another one is destroyed
  std::list<Entry> entries_;
  // the entry whose source holds the session, by serial
  std::optional<std::uint64_t> owner_;
  std::string sessionId_;
  std::uint64_t sessions_ = 0;
  std::uint64_t serials_ = 0;
  std::uint64_t setAsides_ = 0;
  MediaSession published_;
  int busy_ = 0;
  std::deque<Event> pending_;
};

}  // namespace cadence::audiod

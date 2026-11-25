#pragma once

#include "audiod/log.h"
#include "audiod/media-source.h"
#include "backend/pcm-input.h"

#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

// UrlPlay's overlay mode (M4b-D10): a url played once on the overlay input, on the loop thread. it starts no session
// and no MediaState; the music ducks under it as under any overlay audio. one at a time: a newer one stops it and cuts
// what it queued, and so does the gate shutting. an end or a failure lets what it queued play out.
//
// a request is answered as a session's is (Ruling M4b-R5): ok when its audio starts, its code when it fails first,
// failed when it ends first, replaced or refused when a newer one or the gate stops it before that
class MediaOverlay {
 public:
  using Done = std::function<void(std::string_view code)>;

  MediaOverlay(const Log& log, PcmInput& overlay, std::function<bool()> gateOpen, MediaSourceFactory factory);
  ~MediaOverlay();

  MediaOverlay(const MediaOverlay&) = delete;
  MediaOverlay& operator=(const MediaOverlay&) = delete;

  // a refusal comes back at once (refused while the gate is shut, or the factory's code) and done is dropped;
  // kMediaOk: it plays, replacing the one before, and done is called once, later. never once it is destroyed
  std::string_view play(const UrlRequest& request, Done done);
  void gateShut();

 private:
  class Events;
  enum class EventKind : std::uint8_t { Playing, Ended, Failed };
  struct Event {
    std::uint64_t serial = 0;
    EventKind kind = EventKind::Playing;
    std::string code;
  };
  struct Current {
    std::uint64_t serial = 0;
    // what its logs call it
    std::string logId;
    std::shared_ptr<Events> events;
    std::unique_ptr<MediaSource> source;
    Done done;
  };
  // every entry point runs inside one, so a report from inside a call into the source is handled after it
  class Busy;

  void report(Event event);
  void handle(const Event& event);
  // the one playing stops, and what it queued is cut
  void stopCurrent(std::string_view code, std::string_view reason);
  static void answer(Current& current, std::string_view code);

  const Log& log_;
  PcmInput& overlay_;
  std::function<bool()> gateOpen_;
  MediaSourceFactory factory_;
  std::optional<Current> current_;
  std::uint64_t serials_ = 0;
  int busy_ = 0;
  std::deque<Event> pending_;
};

}  // namespace cadence::audiod

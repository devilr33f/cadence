#pragma once

#include "cadence/screen/v1/screen.pb.h"

#include <cstdint>
#include <functional>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// what a screen shows, for the agent (W0-C item 13): refs only (the server issued them to this screen), never titles
struct ScreenContextData {
  cadence_v2_ScreenRoute route = cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED;
  std::string pageRef;
  std::string focusedRef;
  // at most 24, reading order
  std::vector<std::string> visibleRefs;
  std::string source;
  std::string voiceover;
  std::string quality;
  std::string episode;
  std::string query;
  bool drawerOpen = false;
  // the player's part (stream i2): the item playing, its position when the context was taken, its tracks
  struct Track {
    std::string id;
    cadence_v2_MediaTrackKind kind = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED;
    std::string language;
    std::string label;
    bool selected = false;
    bool external = false;
    bool operator==(const Track&) const = default;
  };
  bool hasPlayer = false;
  std::string playerItemRef;
  std::uint64_t positionMs = 0;
  std::vector<Track> tracks;

  bool operator==(const ScreenContextData&) const = default;
};

inline constexpr std::size_t kMaxVisibleRefs = 24;
inline constexpr std::int64_t kContextDebounceMs = 500;
inline constexpr std::int64_t kContextMaxWaitMs = 2000;

// sends ScreenContext to cadenced (which relays it to the server, memory-only there): 500 ms after the last change,
// and at once at a turn's start. each send has a revision one higher, so the server drops a late one
class ContextPublisher {
 public:
  using Send = std::function<bool(const cadence_screen_v1_Envelope&)>;
  explicit ContextPublisher(Send send) : send_(std::move(send)) {}

  void set(const ScreenContextData& data, std::int64_t nowMs);
  // a turn started: the current context goes now, changed or not
  void flush();
  void update(std::int64_t nowMs);

  [[nodiscard]] const ScreenContextData& current() const { return current_; }
  [[nodiscard]] std::uint64_t revision() const { return revision_; }
  [[nodiscard]] std::size_t sent() const { return sent_; }
  [[nodiscard]] bool pending() const { return dueMs_ >= 0; }

 private:
  bool send();

  Send send_;
  ScreenContextData current_;
  bool haveCurrent_ = false;
  std::int64_t dueMs_ = -1;
  std::int64_t firstMs_ = 0;
  std::uint64_t revision_ = 0;
  std::size_t sent_ = 0;
};

}  // namespace cadence::screen::routes

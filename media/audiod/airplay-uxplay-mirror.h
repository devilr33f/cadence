#pragma once

#include <cstddef>
#include <cstdint>
#include <functional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

// the mirror stream to the screen: each access unit libairplay hands over (annex b, the codec config prepended after a
// change) is cut into its config (sps/pps, or vps/sps/pps) and the frame, and goes to the screen on audiod's tap as
// TapEnvelope{mirror_frame}, one per message, with the pts on the presentation clock's media timeline (the sender's
// clock, ns). the screen decodes and shows each frame when the clock says its media time is heard. the config goes
// first, again on each change and to a screen that (re)connects, and after anything is lost no frame goes until the next
// keyframe, so the screen's decoder never sees a frame whose references it lacks
namespace cadence::audiod::uxplay {

struct AccessUnit {
  // the parameter sets, start codes included; empty when the unit carries none
  std::vector<std::uint8_t> config;
  // the rest, start codes included
  std::vector<std::uint8_t> frame;
  bool keyframe = false;
  // false for a unit libairplay could not decrypt or parse (its first byte set to 1), or one not in annex b
  bool valid = false;
};

AccessUnit splitAccessUnit(std::span<const std::uint8_t> annexB, bool h265);

// TapEnvelope{mirror_frame}'s bytes
std::vector<std::uint8_t> encodeMirrorFrame(std::string_view sessionId, std::int64_t ptsNs, bool config, bool keyframe,
                                            std::span<const std::uint8_t> data);

// the most of a unit one tap message carries: a larger frame goes as fragments (MirrorFrame's seq, offset and
// total_size), each message under the 256 KiB the contract keeps them to
inline constexpr std::size_t kMirrorFragmentBytes = 240 * 1024;

struct MirrorFragment {
  std::uint32_t seq = 0;
  std::uint32_t offset = 0;
  std::uint32_t total = 0;
};

// one fragment of a unit: data is unit[offset, offset + data.size()) of its total bytes
std::vector<std::uint8_t> encodeMirrorFrame(std::string_view sessionId, std::int64_t ptsNs, bool config, bool keyframe,
                                            std::span<const std::uint8_t> data, MirrorFragment fragment);

enum class SendResult : std::uint8_t { Sent, Dropped, NoClient };

// the tap's side of it: one encoded frame as one message
using MirrorSend = std::function<SendResult(std::span<const std::uint8_t> encoded)>;

// what reaches the screen of one mirror session, on one thread (the loop's)
class MirrorForwarder {
 public:
  explicit MirrorForwarder(MirrorSend send) : send_(std::move(send)) {}

  void start(std::string sessionId);
  void stop();
  void unit(const AccessUnit& unit, std::int64_t ptsNs);
  // a screen connected to the tap: it gets the config again, then frames from the next keyframe
  void clientConnected();

  [[nodiscard]] bool active() const { return !sessionId_.empty(); }
  [[nodiscard]] std::uint64_t sent() const { return sent_; }
  // frames not sent: no screen, a full socket, one too large for it, or waiting for a keyframe
  [[nodiscard]] std::uint64_t dropped() const { return dropped_; }

 private:
  bool sendConfig(std::int64_t ptsNs);
  // the frame whole, or as fragments past kMirrorFragmentBytes; false once one is not sent
  bool sendFrame(const AccessUnit& unit, std::int64_t ptsNs);

  MirrorSend send_;
  std::string sessionId_;
  std::vector<std::uint8_t> config_;
  bool configPending_ = false;
  bool waitKeyframe_ = true;
  // the units' numbers, for their fragments
  std::uint32_t seq_ = 0;
  std::uint64_t sent_ = 0;
  std::uint64_t dropped_ = 0;
};

}  // namespace cadence::audiod::uxplay

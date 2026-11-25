#pragma once

#include <chrono>
#include <cstddef>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

// airplay's pin pairing on a screen (design §23): with a pin required, a sender that never paired asks for one
// (pair-pin-start), the screen shows it, the user types it on the sender, and pair-setup-pin checks it inside
// libairplay. a sender that paired is remembered by its ed25519 public key, so it connects without a pin from then on.
// the pin is never logged, nor stored; the keys only in the paired file (mode 0600, under the receiver's scratch)
namespace cadence::audiod::uxplay {

// how long a pin stays on screen: a sender that has not paired by then asks again
inline constexpr std::chrono::seconds kPinShown{60};
// the most senders remembered; the oldest goes first
inline constexpr std::size_t kMaxPaired = 64;

// the paired senders' keys, one base64 key per line. loads what the file holds (lines that are no key are skipped),
// and writes the whole set again on each change (a temporary file renamed over it). an empty path keeps them in memory
class PairedSenders {
 public:
  explicit PairedSenders(std::string path);

  [[nodiscard]] bool known(std::string_view key) const;
  // false when it could not be written (it is still known until the receiver restarts)
  bool add(std::string_view key);
  void clear();
  [[nodiscard]] std::size_t size() const { return keys_.size(); }

 private:
  bool save() const;

  std::string path_;
  std::vector<std::string> keys_;
};

// a key as libairplay hands it: base64 of 32 bytes
bool plausibleKey(std::string_view key);

// the pin on screen, on one thread
class PinPrompt {
 public:
  using Clock = std::chrono::steady_clock;

  // a sender asked: shown until it pairs, connects, goes, or kPinShown passes. false for a pin that is no 4 digits
  bool show(std::string_view pin, Clock::time_point now);
  // what ends it: true when a pin was shown
  bool hide();
  // true once, when the shown pin expired
  bool expire(Clock::time_point now);

  [[nodiscard]] bool active() const { return pin_.has_value(); }
  [[nodiscard]] const std::string& pin() const;
  [[nodiscard]] std::optional<Clock::time_point> deadline() const;

 private:
  std::optional<std::string> pin_;
  Clock::time_point shownAt_{};
};

}  // namespace cadence::audiod::uxplay

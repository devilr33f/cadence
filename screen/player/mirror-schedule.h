#pragma once

#include <cstdint>
#include <span>
#include <string>
#include <vector>

namespace cadence::screen::player {

// the airplay mirror presenter's rules (tv stream j), with no decoder in it, so the host tests hold them:
//   1. a frame is the tap's field 4 alone (the tap client decodes it on its own);
//   2. config frames first: nothing reaches a decoder before a session's config, nor after a config before the next
//      keyframe; a config with other bytes makes the decoder again, the same bytes repeated are skipped;
//   3. each output is released at clock.mono_ns + (pts - clock.media_ns) (the presentation clock, its output latency
//      included), straight away while no clock has anchored;
//   4. a late output is dropped, never shown;
//   6. a clock that cannot be the video's never blanks the screen: an output due more than kMaxAheadNs ahead, or the
//      next after kMaxLateRun late ones in a row, is shown at once (unsynced) instead of held or dropped;
//   5. MirrorStop ends the session: its later frames are skipped until a MirrorStart names it again;
//   7. a clock whose latency is longer than kDecodeAheadNs carries a group's lead (a tv that leads a multiroom group:
//      its sound, and so its pictures, come the group's latency later): an access unit waits before the decoder until
//      it is due within kDecodeAheadNs, as the decoder's few outputs could not hold it that long (one due more than
//      kMaxWaitNs ahead is no group's: it decodes at once, and rule 6 shows it).
// frames of another session than the one shown start that session (a sender that reconnected), as the tap and the
// link are separate sockets and a session's frames can come before its MirrorStart. not thread-safe: the presenter
// holds it under its own lock
class MirrorSchedule {
 public:
  // how close to its time an output is handed to the decoder (MediaCodec takes a release time up to a second ahead,
  // but each held output is a buffer the decoder cannot fill), and how late one may be and still be shown
  static constexpr std::int64_t kLeadNs = 50'000'000;
  static constexpr std::int64_t kLateNs = 8'000'000;
  // no sender's latency puts a frame this far after its pts is heard; MediaCodec's release time takes a second at most
  static constexpr std::int64_t kMaxAheadNs = 1'000'000'000;
  // about half a second of frames at a mirror's 30 fps
  static constexpr int kMaxLateRun = 15;
  // rule 7: how far ahead of its time a unit decodes under a group's lead (a group's latency is 200 ms at least, past
  // the output's own), and the longest it waits (a group's latency is 5 s at most)
  static constexpr std::int64_t kDecodeAheadNs = 200'000'000;
  static constexpr std::int64_t kMaxWaitNs = 6'000'000'000;

  enum class Input {
    // not for the decoder
    Skip,
    // a new config: make the decoder again (codec and size from it) and give it these bytes as codec config
    Configure,
    // an access unit for the decoder
    Decode,
  };
  Input frame(const std::string& sessionId, bool config, bool keyframe, std::span<const std::uint8_t> data);

  void start(const std::string& sessionId);
  // true when it ended the session shown (the decoder goes); an empty id ends whatever is shown
  bool stop(const std::string& sessionId);
  // the queue overflowed: frames were lost, so nothing decodes until the next keyframe
  void lost();

  // the presentation clock: mediaNs sounding at monoNs, latencyNs after it went to the output (a group's lead in it on
  // a tv that leads one)
  void clock(std::int64_t mediaNs, std::int64_t monoNs, std::int64_t latencyNs = 0);
  [[nodiscard]] bool anchored() const { return anchored_; }

  struct Release {
    enum class Kind { Render, RenderNow, Drop, Hold } kind = Kind::Hold;
    // Render's and Hold's CLOCK_MONOTONIC time (the clock's, also for an unsynced RenderNow)
    std::int64_t atNs = 0;
    // shown at once although a clock anchored: rule 6
    bool unsynced = false;
  };
  // what to do with a decoded output of that pts at nowNs (CLOCK_MONOTONIC); it counts the late run (rule 6)
  Release output(std::int64_t ptsNs, std::int64_t nowNs);
  // rule 7: how long the unit of that pts still waits before the decoder at nowNs; 0: it goes now
  [[nodiscard]] std::int64_t waitBeforeDecode(std::int64_t ptsNs, std::int64_t nowNs) const;

  [[nodiscard]] const std::string& session() const { return session_; }
  [[nodiscard]] bool active() const { return !session_.empty(); }

 private:
  std::string session_;
  std::vector<std::string> ended_;
  std::vector<std::uint8_t> config_;
  bool configured_ = false;
  bool needKeyframe_ = true;
  bool anchored_ = false;
  int lateRun_ = 0;
  std::int64_t mediaNs_ = 0;
  std::int64_t monoNs_ = 0;
  std::int64_t latencyNs_ = 0;
};

// the codec an annex b config names by its first nal: h264's sps (type 7) or h265's vps/sps (32, 33)
enum class MirrorCodec { Unknown, H264, H265 };
MirrorCodec codecOfConfig(std::span<const std::uint8_t> annexB);

}  // namespace cadence::screen::player

#pragma once

#include <cstddef>
#include <span>
#include <vector>

// a timed stream's audio on its way into the music input (an airplay sender's, a group's), spliced where its placer
// corrects it: a hard cut in a waveform (frames dropped to catch up, silence before a packet, a lost packet's gap) is a
// click, so the splicer holds the last few ms back and joins the two sides with a short raised-cosine fade. what it
// emits is pushed as it comes
namespace cadence::dsp {

// 2.9 ms at 44.1 kHz: long enough that a join is no click, short enough that no note is heard to dip
inline constexpr std::size_t kSpliceFrames = 128;

class Splicer {
 public:
  explicit Splicer(std::size_t channels, std::size_t fadeFrames = kSpliceFrames);

  // frames that follow on from what came before
  void append(std::span<const float> interleaved);
  // a packet whose first frames are dropped: the audio before is crossfaded into what is kept (the dropped frames are
  // the old audio's own continuation), so the join is seamless; with too little kept, a fade out and in
  void drop(std::span<const float> interleaved, std::size_t frames);
  // a gap: what came before fades out, then frames of silence, and the next audio fades in
  void silence(std::size_t frames);
  // a new stream, a flush: nothing is held, and the next audio fades in from silence
  void reset();

  // what is ready to push, interleaved; take it before the next call, which clears it
  [[nodiscard]] std::span<const float> output() const { return out_; }
  void clearOutput() { out_.clear(); }
  // frames held back: what is pushed next is heard this much later
  [[nodiscard]] std::size_t heldFrames() const { return held_.size() / channels_; }

 private:
  [[nodiscard]] float gain(std::size_t index) const;
  // the held frames fade out into out_, and the next audio fades in
  void cut();
  // in after held_, keeping the last fade's worth back
  void emit(std::span<const float> interleaved);

  std::size_t channels_;
  std::size_t fade_;
  std::vector<float> held_;
  std::vector<float> out_;
  std::vector<float> joined_;
  std::vector<float> cross_;
  // frames of the next audio still to fade in, from fade_ down (0: none)
  std::size_t fadeIn_ = 0;
  // a packet dropped whole: the join waits for the next audio
  bool cutPending_ = false;
};

}  // namespace cadence::dsp

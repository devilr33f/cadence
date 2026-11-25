#pragma once

#include "audio/music-levels.h"
#include "audio-layout.h"

#include <cstdint>
#include <memory>
#include <optional>
#include <span>

namespace cadence {

// measures the digital speaker references of the interleaved capture stream: a fullband level and three
// butterworth band energies, bass 40-250 hz, mid 250-2000 hz, treble 2000-12000 hz, over exact 20 ms
// windows. it reads only layout.speakers, never the microphones, and never modifies or copies the capture
// it is given; only the derived 0..1 levels leave it.
//
// one instance belongs to the audio engine's capture thread: process takes each successfully read span, in
// whole frames, and the caller publishes the returned levels directly from that thread. a span that is not
// whole frames is a gap: the partial window is dropped, not padded.
class PlaybackMonitor {
 public:
  PlaybackMonitor();
  ~PlaybackMonitor();

  PlaybackMonitor(const PlaybackMonitor&) = delete;
  PlaybackMonitor& operator=(const PlaybackMonitor&) = delete;

  // accepts 48000 hz, a positive channel count, and nonempty unique in-range reference indices disjoint
  // from the microphone indices; anything else returns false and disables monitoring. it never falls back
  // to microphones, and reconfiguring replaces all previous state
  bool configure(const CaptureLayout& layout);

  // measures whole frames of interleaved pcm16 at the configured channel count, carrying partial windows
  // across reads; a read that spans several windows returns only the newest completed one
  std::optional<MusicLevels> process(std::span<const std::int16_t> interleaved);

  // clears the filters and any partial window
  void reset();

 private:
  struct State;

  MusicLevels finishWindow(State& state);

  std::unique_ptr<State> state_;
};

}  // namespace cadence

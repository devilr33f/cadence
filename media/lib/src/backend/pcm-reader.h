#pragma once

#include <cstddef>
#include <cstdint>
#include <span>

namespace cadence {

// a source the pcm backend reads itself on the playback thread, beside its music input: a player that writes its own
// ring (mpv's ao_cadence). read never locks, allocates or waits
class PcmReader {
 public:
  virtual ~PcmReader() = default;

  // fills stereo (interleaved float 48 kHz, whole frames) with the next frames and silence after them, and returns
  // how many frames it read. heardNs is when the block's first frame will be heard (CLOCK_MONOTONIC), 0 when the
  // device's delay is not known
  virtual std::size_t read(std::span<float> stereo, std::int64_t heardNs) = 0;
};

}  // namespace cadence

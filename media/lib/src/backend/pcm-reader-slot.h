#pragma once

#include "backend/pcm-reader.h"

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <span>
#include <thread>

namespace cadence {

// where the backend finds the reader the session's player writes, if any: set and cleared on a controlling thread,
// read on the playback thread. clear (and a set that replaces one) returns only once the playback thread is out of
// the old reader, so the caller may destroy it then; it waits at most one period
class PcmReaderSlot {
 public:
  void set(PcmReader* reader) {
    reader_.store(reader, std::memory_order_seq_cst);
    while (reading_.load(std::memory_order_seq_cst))
      std::this_thread::yield();
  }
  void clear() { set(nullptr); }
  [[nodiscard]] bool empty() const { return reader_.load(std::memory_order_acquire) == nullptr; }

  // the playback thread: 0 frames and silence when the slot is empty
  std::size_t read(std::span<float> stereo, std::int64_t heardNs) {
    reading_.store(true, std::memory_order_seq_cst);
    auto* reader = reader_.load(std::memory_order_seq_cst);
    std::size_t frames = 0;
    if (reader != nullptr)
      frames = reader->read(stereo, heardNs);
    reading_.store(false, std::memory_order_seq_cst);
    return frames;
  }

 private:
  std::atomic<PcmReader*> reader_{nullptr};
  std::atomic<bool> reading_{false};
};

}  // namespace cadence

#pragma once

#include <algorithm>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <span>
#include <vector>

namespace cadence::aaudio {

// a single-producer single-consumer ring of interleaved s16 frames between audiod's thread and aaudio's callback:
// positions only grow, each side stores its own with release and loads the other's with acquire, so neither ever
// waits. cut moves the reader's position up to a mark the writer sets (a rewind), taken by the reader at its next
// read, so the writer never touches the reader's position while a callback may be copying
class FrameRing {
 public:
  FrameRing() = default;
  FrameRing(std::size_t capacityFrames, std::size_t channels) { reset(capacityFrames, channels); }

  // not while the other side runs
  void reset(std::size_t capacityFrames, std::size_t channels) {
    channels_ = channels;
    capacity_ = capacityFrames;
    data_.assign(capacityFrames * channels, 0);
    read_.store(0, std::memory_order_relaxed);
    write_.store(0, std::memory_order_relaxed);
    cut_.store(0, std::memory_order_relaxed);
  }

  [[nodiscard]] std::size_t capacity() const { return capacity_; }
  [[nodiscard]] std::size_t channels() const { return channels_; }

  // frames waiting, a cut not yet taken counted as gone
  [[nodiscard]] std::size_t fill() const {
    const auto w = write_.load(std::memory_order_acquire);
    const auto r = std::max(read_.load(std::memory_order_acquire), cut_.load(std::memory_order_acquire));
    return w > r ? static_cast<std::size_t>(w - r) : 0;
  }
  [[nodiscard]] std::size_t room() const { return capacity_ - std::min(capacity_, readerFill()); }

  // the writer: copies at most room() frames, returns how many
  std::size_t write(std::span<const std::int16_t> interleaved) {
    const auto w = write_.load(std::memory_order_relaxed);
    const std::size_t n = std::min(interleaved.size() / channels_, room());
    copyIn(w, interleaved.first(n * channels_));
    write_.store(w + n, std::memory_order_release);
    return n;
  }
  // the writer: everything written so far is to be dropped; returns how many frames that takes back
  std::size_t cut() {
    const auto w = write_.load(std::memory_order_relaxed);
    const auto taken = fill();
    cut_.store(w, std::memory_order_release);
    return taken;
  }
  // the reader: the reader's side of a cut, true when one was taken
  bool takeCut() {
    const auto c = cut_.load(std::memory_order_acquire);
    const auto r = read_.load(std::memory_order_relaxed);
    if (c <= r)
      return false;
    read_.store(c, std::memory_order_release);
    return true;
  }
  // the reader: copies at most out.size() / channels frames, returns how many
  std::size_t read(std::span<std::int16_t> out) {
    const auto r = read_.load(std::memory_order_relaxed);
    const auto w = write_.load(std::memory_order_acquire);
    const std::size_t n = std::min(out.size() / channels_, static_cast<std::size_t>(w - r));
    copyOut(r, out.first(n * channels_));
    read_.store(r + n, std::memory_order_release);
    return n;
  }
  // the reader: drops up to frames of the oldest (an input ring that overflowed)
  std::size_t skip(std::size_t frames) {
    const auto r = read_.load(std::memory_order_relaxed);
    const auto w = write_.load(std::memory_order_acquire);
    const std::size_t n = std::min(frames, static_cast<std::size_t>(w - r));
    read_.store(r + n, std::memory_order_release);
    return n;
  }

 private:
  // what the reader still holds, a cut not yet taken included: its frames are not overwritten before it is taken
  [[nodiscard]] std::size_t readerFill() const {
    const auto w = write_.load(std::memory_order_relaxed);
    const auto r = read_.load(std::memory_order_acquire);
    return static_cast<std::size_t>(w - r);
  }
  void copyIn(std::uint64_t at, std::span<const std::int16_t> src) {
    std::size_t done = 0;
    while (done < src.size()) {
      const std::size_t slot = static_cast<std::size_t>((at % capacity_) * channels_);
      const std::size_t n = std::min(src.size() - done, data_.size() - slot);
      std::memcpy(data_.data() + slot, src.data() + done, n * sizeof(std::int16_t));
      done += n;
      at += n / channels_;
    }
  }
  void copyOut(std::uint64_t at, std::span<std::int16_t> dst) {
    std::size_t done = 0;
    while (done < dst.size()) {
      const std::size_t slot = static_cast<std::size_t>((at % capacity_) * channels_);
      const std::size_t n = std::min(dst.size() - done, data_.size() - slot);
      std::memcpy(dst.data() + done, data_.data() + slot, n * sizeof(std::int16_t));
      done += n;
      at += n / channels_;
    }
  }

  std::size_t channels_ = 1;
  std::size_t capacity_ = 0;
  std::vector<std::int16_t> data_;
  std::atomic<std::uint64_t> read_{0};
  std::atomic<std::uint64_t> write_{0};
  std::atomic<std::uint64_t> cut_{0};
};

}  // namespace cadence::aaudio

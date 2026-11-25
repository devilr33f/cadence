#pragma once

// the keyword runner's arena (kws/tflm-model.cpp): tflm's headers, for the runner's own sources alone.
// trust boundary: the headroom below keeps a model that is merely too big from crashing tflm, not a crafted one:
// tflm does not bounds-check what a model asks for, and cadence loads only the allowlisted stock model (audiod's
// sha256 check, before parsing)

#include "tensorflow/lite/micro/arena_allocator/single_arena_buffer_allocator.h"

#include <cstddef>
#include <cstdint>

namespace cadence::kws {

// tflm crashes rather than fail when an allocation finds its arena full (kernels write through the null pointer they
// got, or their prepare asserts on it), so the arena is mapped this much larger than the cap, committed only where
// written, and a build that went past the cap is refused before anything runs. only a model that asks for 16 MiB past
// the cap could still find the arena full
inline constexpr std::size_t kArenaHeadroom = std::size_t{16} << 20;
inline constexpr std::size_t kMaxArenaCap = std::size_t{1} << 30;

// a private anonymous mapping, page aligned: the kernel commits only the pages tflm writes. none for 0 bytes
class ArenaMapping {
 public:
  explicit ArenaMapping(std::size_t bytes);
  ~ArenaMapping();
  ArenaMapping(const ArenaMapping&) = delete;
  ArenaMapping& operator=(const ArenaMapping&) = delete;

  // null when the mapping failed
  [[nodiscard]] std::uint8_t* base() const { return base_; }
  [[nodiscard]] std::size_t size() const { return size_; }

  // the mapping for a cap: the cap in whole pages and the headroom, or none past kMaxArenaCap
  static std::size_t sizeFor(std::size_t cap);

 private:
  std::uint8_t* base_ = nullptr;
  std::size_t size_;
};

// the arena's allocator, on a mapping larger than the cap: it offers no more than the cap leaves (the memory planner
// takes all it is offered), and remembers whether the bytes in use at both ends, temporary ones too, ever passed the
// cap or an allocation failed, which a cap-sized arena would have refused
class ArenaWatch final : public tflite::SingleArenaBufferAllocator {
 public:
  ArenaWatch(std::uint8_t* buffer, std::size_t size, std::size_t cap)
      : SingleArenaBufferAllocator(buffer, size), cap_(cap) {}

  TfLiteStatus ResizeBuffer(std::uint8_t* buffer, std::size_t size, std::size_t alignment) override;
  std::uint8_t* AllocatePersistentBuffer(std::size_t size, std::size_t alignment) override;
  std::uint8_t* AllocateTemp(std::size_t size, std::size_t alignment) override;
  std::size_t GetAvailableMemory(std::size_t alignment) const override;

  [[nodiscard]] bool exceeded() const { return exceeded_; }

 private:
  bool seen(bool ok);

  std::size_t cap_;
  bool exceeded_ = false;
};

}  // namespace cadence::kws

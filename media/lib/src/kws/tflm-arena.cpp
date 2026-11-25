#include "kws/tflm-arena.h"

#include <sys/mman.h>
#include <unistd.h>

#include <algorithm>

namespace cadence::kws {

ArenaMapping::ArenaMapping(std::size_t bytes) : size_(bytes) {
  void* base = bytes == 0 ? MAP_FAILED
                          : mmap(nullptr, size_, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE,
                                 -1, 0);
  base_ = base == MAP_FAILED ? nullptr : static_cast<std::uint8_t*>(base);
}

ArenaMapping::~ArenaMapping() {
  if (base_ != nullptr)
    munmap(base_, size_);
}

std::size_t ArenaMapping::sizeFor(std::size_t cap) {
  if (cap > kMaxArenaCap)
    return 0;
  const auto page = static_cast<std::size_t>(sysconf(_SC_PAGESIZE));
  return (cap + page - 1) / page * page + kArenaHeadroom;
}

TfLiteStatus ArenaWatch::ResizeBuffer(std::uint8_t* buffer, std::size_t size, std::size_t alignment) {
  return seen(SingleArenaBufferAllocator::ResizeBuffer(buffer, size, alignment) == kTfLiteOk) ? kTfLiteOk
                                                                                               : kTfLiteError;
}

std::uint8_t* ArenaWatch::AllocatePersistentBuffer(std::size_t size, std::size_t alignment) {
  auto* buffer = SingleArenaBufferAllocator::AllocatePersistentBuffer(size, alignment);
  seen(buffer != nullptr);
  return buffer;
}

std::uint8_t* ArenaWatch::AllocateTemp(std::size_t size, std::size_t alignment) {
  auto* buffer = SingleArenaBufferAllocator::AllocateTemp(size, alignment);
  seen(buffer != nullptr);
  return buffer;
}

std::size_t ArenaWatch::GetAvailableMemory(std::size_t alignment) const {
  const auto used = GetUsedBytes();
  const auto left = used < cap_ ? (cap_ - used) / alignment * alignment : 0;
  return std::min(SingleArenaBufferAllocator::GetAvailableMemory(alignment), left);
}

bool ArenaWatch::seen(bool ok) {
  exceeded_ = exceeded_ || !ok || GetUsedBytes() > cap_;
  return ok;
}

}  // namespace cadence::kws

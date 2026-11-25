#include "player/pcm-ring.h"

#include <cerrno>
#include <cstddef>
#include <cstring>
#include <fcntl.h>
#include <sys/eventfd.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

// the header's u64 positions cross processes through shared memory: they must be lock-free (armv7 has ldrexd)
static_assert(__atomic_always_lock_free(8, nullptr), "the ring needs lock-free 64-bit atomics");
static_assert(sizeof(cadence_ring_header) <= CADENCE_RING_DATA_OFFSET, "the header outgrew its data offset");
static_assert(offsetof(cadence_ring_header, write_pos) == 64 && offsetof(cadence_ring_header, read_pos) == 128,
              "the writer's and reader's lines are 64 bytes apart");

namespace cadence::screen::player {

namespace {

std::string errnoToken(const char* what) { return std::string(what) + "_errno_" + std::to_string(errno); }

}  // namespace

std::optional<PcmRing> PcmRing::create(std::uint32_t capacity, std::string* error) {
  if (capacity == 0 || (capacity & (capacity - 1)) != 0) {
    if (error != nullptr) *error = "capacity";
    return std::nullopt;
  }
  PcmRing ring;
  ring.bytes_ = cadence_ring_bytes(capacity);
  ring.ringFd_ = ::memfd_create("cadence-ring", MFD_CLOEXEC | MFD_ALLOW_SEALING);
  if (ring.ringFd_ < 0) {
    if (error != nullptr) *error = errnoToken("memfd");
    return std::nullopt;
  }
  if (::ftruncate(ring.ringFd_, static_cast<off_t>(ring.bytes_)) != 0 ||
      ::fcntl(ring.ringFd_, F_ADD_SEALS, F_SEAL_GROW | F_SEAL_SHRINK | F_SEAL_SEAL) != 0) {
    if (error != nullptr) *error = errnoToken("seal");
    return std::nullopt;
  }
  void* map = ::mmap(nullptr, static_cast<std::size_t>(ring.bytes_), PROT_READ | PROT_WRITE, MAP_SHARED, ring.ringFd_, 0);
  if (map == MAP_FAILED) {
    if (error != nullptr) *error = errnoToken("mmap");
    return std::nullopt;
  }
  ring.header_ = static_cast<cadence_ring_header*>(map);
  std::memset(ring.header_, 0, CADENCE_RING_DATA_OFFSET);
  ring.header_->magic = CADENCE_RING_MAGIC;
  ring.header_->version = CADENCE_RING_VERSION;
  ring.header_->rate = CADENCE_RING_RATE;
  ring.header_->channels = CADENCE_RING_CHANNELS;
  ring.header_->capacity = capacity;
  ring.header_->data_offset = CADENCE_RING_DATA_OFFSET;
  ring.eventFd_ = ::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
  if (ring.eventFd_ < 0) {
    if (error != nullptr) *error = errnoToken("eventfd");
    return std::nullopt;
  }
  return ring;
}

std::optional<PcmRing> PcmRing::adopt(int ringFd, int eventFd, std::uint32_t capacity, std::string* error) {
  PcmRing ring;
  ring.ringFd_ = ringFd;
  ring.eventFd_ = eventFd;
  const auto refuse = [&](std::string token) -> std::optional<PcmRing> {
    if (error != nullptr) *error = std::move(token);
    return std::nullopt;
  };
  if (ringFd < 0 || eventFd < 0) return refuse("fds");
  if (capacity == 0 || (capacity & (capacity - 1)) != 0) return refuse("capacity");
  ring.bytes_ = cadence_ring_bytes(capacity);
  struct stat info {};
  if (::fstat(ringFd, &info) != 0) return refuse(errnoToken("fstat"));
  if (info.st_size < 0 || static_cast<std::uint64_t>(info.st_size) < ring.bytes_) return refuse("size");
  void* map = ::mmap(nullptr, static_cast<std::size_t>(ring.bytes_), PROT_READ | PROT_WRITE, MAP_SHARED, ringFd, 0);
  if (map == MAP_FAILED) return refuse(errnoToken("mmap"));
  ring.header_ = static_cast<cadence_ring_header*>(map);
  const auto* h = ring.header_;
  if (h->magic != CADENCE_RING_MAGIC || h->version != CADENCE_RING_VERSION || h->rate != CADENCE_RING_RATE ||
      h->channels != CADENCE_RING_CHANNELS || h->capacity != capacity || h->data_offset != CADENCE_RING_DATA_OFFSET) {
    return refuse("header");
  }
  return ring;
}

PcmRing::PcmRing(PcmRing&& o) noexcept
    : ringFd_(o.ringFd_), eventFd_(o.eventFd_), header_(o.header_), bytes_(o.bytes_) {
  o.ringFd_ = o.eventFd_ = -1;
  o.header_ = nullptr;
}

PcmRing& PcmRing::operator=(PcmRing&& o) noexcept {
  if (this != &o) {
    release();
    ringFd_ = o.ringFd_;
    eventFd_ = o.eventFd_;
    header_ = o.header_;
    bytes_ = o.bytes_;
    o.ringFd_ = o.eventFd_ = -1;
    o.header_ = nullptr;
  }
  return *this;
}

PcmRing::~PcmRing() { release(); }

void PcmRing::release() {
  if (header_ != nullptr) ::munmap(header_, static_cast<std::size_t>(bytes_));
  if (ringFd_ >= 0) ::close(ringFd_);
  if (eventFd_ >= 0) ::close(eventFd_);
  header_ = nullptr;
  ringFd_ = eventFd_ = -1;
}

}  // namespace cadence::screen::player

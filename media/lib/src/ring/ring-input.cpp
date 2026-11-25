#include "ring/ring-input.h"

#include "dsp/format.h"

#include <algorithm>
#include <cstring>
#include <ctime>
#include <fcntl.h>
#include <sys/eventfd.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <unistd.h>

#ifndef MFD_ALLOW_SEALING
#define MFD_ALLOW_SEALING 2U
#endif
#ifndef MFD_CLOEXEC
#define MFD_CLOEXEC 1U
#endif

namespace cadence {

namespace {

constexpr std::uint32_t kMinCapacity = 256;
constexpr std::uint32_t kMaxCapacity = 65536;

// the raw syscall: the same call bionic wraps from api 30, and glibc from 2.27
int makeMemfd(const char* name) {
  return static_cast<int>(syscall(SYS_memfd_create, name, MFD_CLOEXEC | MFD_ALLOW_SEALING));
}

std::int64_t monotonicNs() {
  timespec now{};
  clock_gettime(CLOCK_MONOTONIC, &now);
  return static_cast<std::int64_t>(now.tv_sec) * 1'000'000'000 + now.tv_nsec;
}

std::int64_t framesNs(std::uint64_t frames) {
  return static_cast<std::int64_t>(frames * 1'000'000'000ULL / dsp::kRate);
}

// copies n frames from the ring at pos into out
void copyOut(const float* data, std::uint32_t mask, std::uint64_t pos, std::size_t n, float* out) {
  const auto slot = static_cast<std::uint32_t>(pos) & mask;
  const std::size_t first = std::min<std::size_t>(n, mask + 1 - slot);
  std::memcpy(out, data + std::size_t{slot} * 2, first * 2 * sizeof(float));
  std::memcpy(out + first * 2, data, (n - first) * 2 * sizeof(float));
}

}  // namespace

Result<std::unique_ptr<RingInput>, std::string_view> RingInput::create(std::uint32_t capacity) {
  if (capacity < kMinCapacity || capacity > kMaxCapacity || (capacity & (capacity - 1)) != 0)
    return std::string_view("invalid");
  const int ring = makeMemfd("cadence-ring");
  if (ring < 0)
    return std::string_view("memfd_failed");
  const auto bytes = static_cast<std::size_t>(cadence_ring_bytes(capacity));
  const int event = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
  void* mapped = MAP_FAILED;
  if (event >= 0 && ftruncate(ring, static_cast<off_t>(bytes)) == 0 &&
      fcntl(ring, F_ADD_SEALS, F_SEAL_SHRINK | F_SEAL_GROW) == 0)
    mapped = mmap(nullptr, bytes, PROT_READ | PROT_WRITE, MAP_SHARED, ring, 0);
  if (mapped == MAP_FAILED) {
    close(ring);
    if (event >= 0)
      close(event);
    return std::string_view("ring_failed");
  }
  auto* header = static_cast<cadence_ring_header*>(mapped);
  std::memset(header, 0, CADENCE_RING_DATA_OFFSET);
  header->version = CADENCE_RING_VERSION;
  header->rate = CADENCE_RING_RATE;
  header->channels = CADENCE_RING_CHANNELS;
  header->capacity = capacity;
  header->data_offset = CADENCE_RING_DATA_OFFSET;
  CADENCE_RING_STORE(&header->magic, CADENCE_RING_MAGIC);
  return std::unique_ptr<RingInput>(new RingInput(ring, event, header, bytes));
}

std::uint32_t RingInput::capacityFor(std::size_t burstFrames) {
  const std::size_t wanted = std::max<std::size_t>(2 * burstFrames, dsp::framesFor(120));
  std::uint32_t capacity = kMinCapacity;
  while (capacity < wanted && capacity < kMaxCapacity)
    capacity *= 2;
  return capacity;
}

RingInput::RingInput(int ringFd, int eventFd, cadence_ring_header* header, std::size_t bytes)
    : ringFd_(ringFd),
      eventFd_(eventFd),
      h_(header),
      bytes_(bytes),
      data_(cadence_ring_data(header)),
      mask_(header->capacity - 1),
      refillTarget_(header->capacity / 2) {}

void RingInput::setRefillTarget(std::uint32_t frames) {
  refillTarget_.store(std::min(frames, mask_ + 1), std::memory_order_relaxed);
}

RingInput::~RingInput() {
  munmap(h_, bytes_);
  close(ringFd_);
  close(eventFd_);
}

void RingInput::setOpen(bool open) { open_.store(open, std::memory_order_release); }

void RingInput::setGain(float gain, std::uint32_t rampMs) {
  gain_.store(gain, std::memory_order_relaxed);
  rampMs_.store(rampMs, std::memory_order_relaxed);
  gainSerial_.fetch_add(1, std::memory_order_release);
}

void RingInput::setMuted(bool muted) {
  muted_.store(muted, std::memory_order_relaxed);
  gainSerial_.fetch_add(1, std::memory_order_release);
}

std::size_t RingInput::read(std::span<float> stereo, std::int64_t heardNs) {
  std::fill(stereo.begin(), stereo.end(), 0.0f);
  if (!open_.load(std::memory_order_acquire))
    return 0;
  if (const auto serial = gainSerial_.load(std::memory_order_acquire); serial != seenGainSerial_) {
    seenGainSerial_ = serial;
    const bool muted = muted_.load(std::memory_order_relaxed);
    // a mute is near-instant: 5 ms, enough to keep it from clicking
    const auto ramp = muted ? 5U : rampMs_.load(std::memory_order_relaxed);
    ramp_.setTarget(muted ? 0.0f : gain_.load(std::memory_order_relaxed), dsp::framesFor(ramp));
  }
  const std::size_t want = stereo.size() / dsp::kStereo;
  // the read position and the seen generation are the reader's alone
  std::uint64_t pos = h_->read_pos;
  reads_.fetch_add(1, std::memory_order_relaxed);
  if (const auto generation = CADENCE_RING_LOAD(&h_->generation); generation != h_->seen_generation) {
    pos = std::max(pos, CADENCE_RING_LOAD(&h_->flush_pos));
    h_->seen_generation = generation;
    refilling_ = true;
    heldFrames_ = 0;
  }
  const auto state = CADENCE_RING_LOAD(&h_->writer_state);
  const std::uint64_t written = state == CADENCE_RING_PAUSED ? pos : CADENCE_RING_LOAD(&h_->write_pos);
  if (refilling_) {
    const std::uint64_t queued = written > pos ? written - pos : 0;
    // a writer that queues less than the target would otherwise be held silent for good (ao_cadence's default
    // buffer against half an 8192-frame ring on the stick): past the bound, what it queued plays
    const bool timedOut = queued > 0 && heldFrames_ >= dsp::framesFor(kMaxRefillHoldMs);
    if (queued < refillTarget_.load(std::memory_order_relaxed) && !timedOut) {
      // silence, the position frozen: the frame at it is heard after this block at the earliest
      CADENCE_RING_STORE(&h_->read_pos, pos);
      heldFrames_ += want;
      heldReads_.fetch_add(1, std::memory_order_relaxed);
      refillingNow_.store(true, std::memory_order_relaxed);
      if (heardNs != 0)
        publish(heardNs + framesNs(want));
      const std::uint64_t one = 1;
      (void)!write(eventFd_, &one, sizeof(one));
      return 0;
    }
    if (timedOut && queued < refillTarget_.load(std::memory_order_relaxed))
      refillTimeouts_.fetch_add(1, std::memory_order_relaxed);
    refilling_ = false;
    refillingNow_.store(false, std::memory_order_relaxed);
  }
  // a writer that ran ahead of a flush it has not published reads as nothing yet
  const std::size_t n = written > pos ? static_cast<std::size_t>(std::min<std::uint64_t>(written - pos, want)) : 0;
  copyOut(data_, mask_, pos, n, stereo.data());
  framesRead_.fetch_add(n, std::memory_order_relaxed);
  if (n == 0)
    emptyReads_.fetch_add(1, std::memory_order_relaxed);
  if (n < want && state == CADENCE_RING_STREAMING)
    __atomic_add_fetch(&h_->underruns, 1, __ATOMIC_RELAXED);
  CADENCE_RING_STORE(&h_->read_pos, pos + n);
  if (n > 0)
    ramp_.apply(stereo.first(n * dsp::kStereo));
  if (heardNs != 0)
    publish(heardNs + framesNs(n));
  const std::uint64_t one = 1;
  (void)!write(eventFd_, &one, sizeof(one));
  return n;
}

std::size_t RingInput::drain(std::span<float> stereo, bool& flushed) {
  flushed = false;
  if (!open_.load(std::memory_order_acquire))
    return 0;
  const std::size_t want = stereo.size() / dsp::kStereo;
  std::uint64_t pos = h_->read_pos;
  reads_.fetch_add(1, std::memory_order_relaxed);
  if (const auto generation = CADENCE_RING_LOAD(&h_->generation); generation != h_->seen_generation) {
    pos = std::max(pos, CADENCE_RING_LOAD(&h_->flush_pos));
    h_->seen_generation = generation;
    flushed = true;
    // the mixer's refill hold is for a seek it plays itself: the pump's group buffers its own start
    refilling_ = false;
    refillingNow_.store(false, std::memory_order_relaxed);
  }
  const auto state = CADENCE_RING_LOAD(&h_->writer_state);
  const std::uint64_t written = state == CADENCE_RING_PAUSED ? pos : CADENCE_RING_LOAD(&h_->write_pos);
  const std::size_t n = written > pos ? static_cast<std::size_t>(std::min<std::uint64_t>(written - pos, want)) : 0;
  copyOut(data_, mask_, pos, n, stereo.data());
  framesRead_.fetch_add(n, std::memory_order_relaxed);
  if (n == 0)
    emptyReads_.fetch_add(1, std::memory_order_relaxed);
  CADENCE_RING_STORE(&h_->read_pos, pos + n);
  if (n > 0 || flushed) {
    const std::uint64_t one = 1;
    (void)!write(eventFd_, &one, sizeof(one));
  }
  return n;
}

std::uint64_t RingInput::readPosition() const { return CADENCE_RING_LOAD(&h_->read_pos); }

void RingInput::publish(std::int64_t heardNs) { publishHeard(h_->read_pos, heardNs); }

void RingInput::publishHeard(std::uint64_t pos, std::int64_t heardNs) {
  const auto now = monotonicNs();
  const std::uint32_t seq = h_->seq;
  CADENCE_RING_STORE(&h_->seq, seq + 1);
  __atomic_thread_fence(__ATOMIC_RELEASE);
  __atomic_store_n(&h_->read_pos_stamped, pos, __ATOMIC_RELAXED);
  __atomic_store_n(&h_->heard_ns, heardNs, __ATOMIC_RELAXED);
  __atomic_store_n(&h_->stamp_ns, now, __ATOMIC_RELAXED);
  __atomic_store_n(&h_->latency_ns, heardNs - now, __ATOMIC_RELAXED);
  CADENCE_RING_STORE(&h_->seq, seq + 2);
}

RingStatus RingInput::status() const {
  RingStatus s;
  s.readPos = CADENCE_RING_LOAD(&h_->read_pos);
  s.writePos = CADENCE_RING_LOAD(&h_->write_pos);
  s.generation = CADENCE_RING_LOAD(&h_->generation);
  s.writerState = CADENCE_RING_RELAXED(&h_->writer_state);
  s.underruns = CADENCE_RING_RELAXED(&h_->underruns);
  s.latencyNs = CADENCE_RING_RELAXED(&h_->latency_ns);
  s.framesRead = framesRead_.load(std::memory_order_relaxed);
  s.reads = reads_.load(std::memory_order_relaxed);
  s.emptyReads = emptyReads_.load(std::memory_order_relaxed);
  s.heldReads = heldReads_.load(std::memory_order_relaxed);
  s.refillTimeouts = refillTimeouts_.load(std::memory_order_relaxed);
  s.open = open_.load(std::memory_order_relaxed);
  s.refilling = refillingNow_.load(std::memory_order_relaxed);
  s.muted = muted_.load(std::memory_order_relaxed);
  s.gain = gain_.load(std::memory_order_relaxed);
  return s;
}

std::unique_ptr<RingWriter> RingWriter::attach(int ringFd) {
  struct stat info {};
  if (fstat(ringFd, &info) != 0 || info.st_size < static_cast<off_t>(CADENCE_RING_DATA_OFFSET))
    return nullptr;
  const auto bytes = static_cast<std::size_t>(info.st_size);
  void* mapped = mmap(nullptr, bytes, PROT_READ | PROT_WRITE, MAP_SHARED, ringFd, 0);
  if (mapped == MAP_FAILED)
    return nullptr;
  auto* header = static_cast<cadence_ring_header*>(mapped);
  if (CADENCE_RING_LOAD(&header->magic) != CADENCE_RING_MAGIC || header->version != CADENCE_RING_VERSION ||
      cadence_ring_bytes(header->capacity) > bytes) {
    munmap(mapped, bytes);
    return nullptr;
  }
  return std::unique_ptr<RingWriter>(new RingWriter(header, bytes));
}

RingWriter::RingWriter(cadence_ring_header* header, std::size_t bytes)
    : h_(header), bytes_(bytes), data_(cadence_ring_data(header)), mask_(header->capacity - 1) {}

RingWriter::~RingWriter() { munmap(h_, bytes_); }

std::size_t RingWriter::write(std::span<const float> stereo) {
  const std::uint64_t pos = h_->write_pos;
  const std::uint64_t read = CADENCE_RING_LOAD(&h_->read_pos);
  const std::uint64_t room = std::uint64_t{mask_} + 1 - (pos - read);
  const auto n = static_cast<std::size_t>(std::min<std::uint64_t>(room, stereo.size() / dsp::kStereo));
  const auto slot = static_cast<std::uint32_t>(pos) & mask_;
  const std::size_t first = std::min<std::size_t>(n, mask_ + 1 - slot);
  std::memcpy(data_ + std::size_t{slot} * 2, stereo.data(), first * 2 * sizeof(float));
  std::memcpy(data_, stereo.data() + first * 2, (n - first) * 2 * sizeof(float));
  CADENCE_RING_STORE(&h_->write_pos, pos + n);
  return n;
}

void RingWriter::flush() {
  CADENCE_RING_STORE(&h_->flush_pos, CADENCE_RING_RELAXED(&h_->write_pos));
  CADENCE_RING_STORE(&h_->generation, h_->generation + 1);
}

void RingWriter::setState(std::uint32_t state) { CADENCE_RING_STORE(&h_->writer_state, state); }

bool RingWriter::timestamp(std::uint64_t& pos, std::int64_t& heardNs) const {
  return cadence_ring_timestamp(h_, &pos, &heardNs) != 0;
}

std::uint64_t RingWriter::written() const { return CADENCE_RING_RELAXED(&h_->write_pos); }

}  // namespace cadence

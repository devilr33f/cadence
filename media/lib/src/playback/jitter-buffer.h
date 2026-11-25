#pragma once

#include "trace/trace.h"

#include <array>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <mutex>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

namespace cadence {

struct JitterBufferConfig {
  // 48 kHz mono: a response starts once 100 ms is queued, and no more than 500 ms is ever queued
  std::size_t targetSamples = 4800;
  std::size_t maxSamples = 24000;
  // how many cancelled, played out, or dropped response ids are remembered so their late packets are refused
  std::size_t retiredIds = 64;
};

enum class PushResult {
  Accepted,
  // not the latest response, or it already ended
  Stale,
  // would exceed the maximum, so the whole packet was dropped
  Overflow,
};

// the responses a read began, and the one that ran dry, by id: what the player marks (trace/trace.h). filled only
// with CADENCE_TRACING on
struct JitterTrace {
  // a 20 ms read may end one short response and begin the next; a third begun in the same read goes unmarked
  static constexpr std::size_t kStarts = 2;
  // where each begun response's first sample sits in the read's output
  std::array<std::size_t, kStarts> startOffsets{};
  std::array<trace::Id, kStarts> startIds{};
  std::size_t starts = 0;
  // set with JitterRead::underrun
  trace::Id underrun;
};

struct JitterRead {
  // response samples at the front of the output; the rest is silence
  std::size_t samples = 0;
  // the playing response ran dry before it ended
  bool underrun = false;
  // an ended response's last sample was read and the response retired
  bool drained = false;
  // set only in the read that retired the watched response: the offset just past its last sample in this
  // read's output, which may be followed by later responses' samples or silence. nothing here says the
  // sample was written to the device, let alone heard
  std::optional<std::size_t> marker;
  // generation of the queue this read came from
  std::uint64_t generation = 0;
  JitterTrace trace;
};

// counters since construction; never audio
struct JitterBufferStatus {
  // the latest response, which takes packets until it ends
  std::optional<std::string> activeResponse;
  bool ended = false;
  // the response being read reached the target and is still playing
  bool playing = false;
  // of every queued response
  std::size_t queuedSamples = 0;
  std::uint64_t generation = 0;
  std::uint64_t staleRejected = 0;
  std::uint64_t overflowDropped = 0;
  std::uint64_t underruns = 0;
  std::uint64_t drained = 0;
};

// the playback queue of responses (48 kHz mono). only the latest response's packets are queued. a response
// that begins after the latest one ended queues behind it, so a finished reply always plays out; a cancel, or
// a response that begins while the latest is still arriving, drops everything queued, and cancelFrom drops a
// response with the ones queued after it. every response that is cancelled, played out, or dropped is retired
// and its packets are refused from then on. the generation
// increases whenever audio already read may belong to a response that must no longer be heard, so the player
// knows to discard what the device still holds. safe to call from any thread.
class JitterBuffer {
 public:
  explicit JitterBuffer(JitterBufferConfig config = {});

  // response_start: false for a retired or queued id. queues behind the latest response if it ended and
  // drops everything queued otherwise
  bool begin(std::string_view responseId);
  // never blocks
  PushResult push(std::string_view responseId, std::span<const std::int16_t> pcm);
  // response_end: no more packets, and what is queued plays out even below the target.
  // false unless the response is the latest and not already ended
  bool end(std::string_view responseId);
  // one audible-completion watch at a time, armed by the player after begin took the response: the read
  // that retires this response reports its last-sample boundary as JitterRead::marker. only an exact
  // still-queued key is accepted: a retired or never-begun id can never produce a boundary. a cancel
  // drops the watch with the queued audio
  bool watch(std::string_view responseId);
  // no active or queued voice response, without building a status (no per-period allocation)
  [[nodiscard]] bool empty() const;
  // a queued id drops every queued response. an id that is not queued is only retired, so playback carries
  // on. without an id, everything queued stops and audio already read is always marked stale. true when the
  // generation increased
  bool cancel(std::optional<std::string_view> responseId = std::nullopt);
  // a queued id drops that response and every one queued after it; the ones before it play on. only the front
  // is ever read, so the generation increases only when something of a dropped response was read, or a dropped
  // one is watched (so its watch settles). an id that is not queued is only retired. true when the generation
  // increased
  bool cancelFrom(std::string_view responseId);
  // fills the whole output with queued audio in order and silence after it. a response plays once the target
  // is queued or it has ended; a sample is read at most once
  JitterRead read(std::span<std::int16_t> output);

  [[nodiscard]] std::uint64_t generation() const;
  [[nodiscard]] JitterBufferStatus status() const;

 private:
  struct Response {
    std::string id;
    std::size_t samples = 0;
    bool ended = false;
    // a sample of it was read
    bool read = false;
  };

  void retire(std::string id);
  [[nodiscard]] bool retired(std::string_view id) const;
  [[nodiscard]] bool queued(std::string_view id) const;
  [[nodiscard]] bool acceptsPackets(std::string_view id) const;
  // drops queued audio and retires every queued response
  void dropAll();

  const JitterBufferConfig config_;
  mutable std::mutex mutex_;
  // a ring of maxSamples holding every queued response's audio in order
  std::vector<std::int16_t> ring_;
  std::size_t head_ = 0;
  std::size_t queued_ = 0;
  // oldest first; every response but the last has ended
  std::deque<Response> responses_;
  bool playing_ = false;
  // the response whose drain read reports a marker; one-shot, cleared when reported or dropped
  std::optional<std::string> watched_;
  std::deque<std::string> retired_;
  JitterBufferStatus counters_;
};

}  // namespace cadence

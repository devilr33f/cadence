#pragma once

#include "capture/audio-engine.h"
#include "support/eventually.h"
#include "support/fake-pcm.h"
#include "support/fake-stock.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <future>
#include <map>
#include <mutex>
#include <set>
#include <string>
#include <vector>

namespace cadence::engine_harness {

using test_support::eventually;

// holds the processing thread inside a vqe process call. declare it before the engine and its
// releaseOnExit() guard after, so a failed check never leaves the engine joining a held thread
class ProcessStall {
 public:
  struct Guard {
    ProcessStall& stall;
    ~Guard() { stall.release(); }
  };

  // call once from duringProcess, on the processing thread
  void hold();
  // true once the processing thread is held
  [[nodiscard]] bool held();
  void release();
  [[nodiscard]] Guard releaseOnExit() { return Guard{*this}; }

 private:
  std::promise<void> held_;
  std::future<void> heldSignal_ = held_.get_future();
  std::promise<void> release_;
  std::shared_future<void> released_ = release_.get_future().share();
  std::once_flag releaseOnce_;
};

// callback sink; callbacks arrive on the engine's processing thread, except the playback levels, which
// arrive on the capture thread (and once more synchronously from stop)
class Recorder {
 public:
  // runs inside onPcm16 after each frame is recorded
  std::function<void()> afterFrame;

  AudioEngineCallbacks callbacks();
  [[nodiscard]] int wakes() const;
  [[nodiscard]] std::vector<std::string> commands() const;
  [[nodiscard]] std::vector<std::vector<std::int16_t>> frames() const;
  // every delivered frame, concatenated
  [[nodiscard]] std::vector<std::int16_t> samples() const;
  // every published playback level, in publish order
  [[nodiscard]] std::vector<MusicLevels> playbackLevels() const;

 private:
  mutable std::mutex mutex_;
  int wakes_ = 0;
  std::vector<std::string> commands_;
  std::vector<std::vector<std::int16_t>> frames_;
  std::vector<MusicLevels> playbackLevels_;
};

// fake capture and stock engines. vqe output 9 (stt) counts up across chunks, so delivered frames
// show exactly which samples they hold: sample k of chunk n is (n - 1) * outputFrames + k
class EngineFixture : public fake_stock::StockFixture {
 public:
  EngineFixture();

  // vqe chunks are counted from 1 in process-call order; set before start
  std::set<int> wakeChunks;
  // chunk -> the phrase the command component hears in it; never a wake chunk too
  std::map<int, std::string> commandChunks;
  std::set<int> failingChunks;
  // also runs inside each process call
  std::function<void()> duringProcess;

  fake_pcm::FakePcmSource source;
  Recorder recorder;

  // keeps 48 kHz in, 16 kHz out
  void useOutputFrames(int frames);
  [[nodiscard]] AudioEngineConfig config() const;
  // config() with the combined spotter that also hears command words
  [[nodiscard]] AudioEngineConfig configWithCommands() const;
  [[nodiscard]] std::size_t chunkFrames() const { return static_cast<std::size_t>(stock.chunkFrames); }
  [[nodiscard]] std::int16_t sttSample(std::size_t chunk, std::size_t index) const;
  // the stt samples of chunks first..last, in order
  [[nodiscard]] std::vector<std::int16_t> sttSamples(std::size_t first, std::size_t last) const;

  // queues whole vqe chunks with a 0.25 amplitude reference tone in every speaker channel and silence in
  // the microphones, then waits until they are handled; the playback monitor keeps its own 20 ms windows
  AudioEngineStatus feedReferenceTone(const AudioEngine& engine, std::size_t chunks);

  // queues whole vqe chunks of capture holding value in every channel and waits until they are handled;
  // batches stay well under the one-second fifo however slowly the engine threads run
  AudioEngineStatus feed(const AudioEngine& engine, std::size_t chunks, std::int16_t value = 100);
  // waits until the engine has handled this many chunks since start
  AudioEngineStatus handled(const AudioEngine& engine, std::uint64_t chunks) const;
};

}  // namespace cadence::engine_harness

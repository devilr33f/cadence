#pragma once

#include "audio/opus-codec.h"
#include "audiod/output.h"
#include "file/wav.h"
#include "playback/volume-control.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <mutex>
#include <numbers>
#include <optional>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

// cadenced's side of audiod's playback messages, for the suites that drive Playback over its socket
namespace cadence::test_support {

using audiod::Envelope;
using audiod::OutputConfig;
using audiod::OutputFactory;
using audiod::UniqueFd;
using Packet = std::vector<std::uint8_t>;
using StreamDone = cadence_audiod_v1_StreamDone;

inline constexpr auto kFile = cadence_audiod_v1_Backend_BACKEND_FILE;

inline std::string event(std::string_view msg) { return "\"msg\":\"" + std::string(msg) + "\""; }

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  REQUIRE(text.size() < N);
  std::memcpy(out, text.data(), text.size());
  out[text.size()] = '\0';
}

inline Envelope configureOutput(const std::string& dir, std::string_view mixerControl = {}, std::uint32_t channels = 0) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_configure_tag;
  auto& output = envelope.body.configure.output;
  envelope.body.configure.has_output = true;
  output.backend = kFile;
  copyText(output.dir, dir);
  copyText(output.mixer_control, mixerControl);
  output.channels = channels;
  return envelope;
}

inline Envelope volumeSet(std::uint32_t level, bool transient = false) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_volume_set_tag;
  envelope.body.volume_set.level = level;
  envelope.body.volume_set.transient = transient;
  return envelope;
}

inline Envelope streamStart(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_stream_start_tag;
  copyText(envelope.body.stream_start.stream_id, id);
  envelope.body.stream_start.channels = 1;
  return envelope;
}

inline Envelope audioDown(std::string_view id, std::uint32_t seq, const Packet& opus) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_audio_down_tag;
  auto& body = envelope.body.audio_down;
  copyText(body.stream_id, id);
  body.seq = seq;
  REQUIRE(opus.size() <= sizeof body.opus.bytes);
  std::memcpy(body.opus.bytes, opus.data(), opus.size());
  body.opus.size = static_cast<pb_size_t>(opus.size());
  return envelope;
}

inline Envelope streamEnd(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_stream_end_tag;
  copyText(envelope.body.stream_end.stream_id, id);
  return envelope;
}

inline Envelope streamCancel(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_stream_cancel_tag;
  copyText(envelope.body.stream_cancel.stream_id, id);
  return envelope;
}

inline Envelope tone(cadence_audiod_v1_ToneKind kind) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_tone_tag;
  envelope.body.tone.kind = kind;
  return envelope;
}

inline Envelope toneStart(std::string_view id, cadence_audiod_v1_ToneKind kind) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_tone_start_tag;
  copyText(envelope.body.tone_start.id, id);
  envelope.body.tone_start.kind = kind;
  return envelope;
}

inline Envelope toneStop(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_tone_stop_tag;
  copyText(envelope.body.tone_stop.id, id);
  return envelope;
}

// the next frame is a ToneState of id with that outcome
inline void expectToneState(int fd, std::string_view id, bool sounding, std::string_view code) {
  const auto state = expectFrame(fd, cadence_audiod_v1_Envelope_tone_state_tag).body.tone_state;
  CHECK(std::string_view(state.id) == id);
  CHECK(state.sounding == sounding);
  CHECK(std::string_view(state.code) == code);
}

inline Envelope duckSet(bool active) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_duck_set_tag;
  envelope.body.duck_set.active = active;
  return envelope;
}

inline Envelope duckModeSettings(cadence_audiod_v1_DuckMode mode) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_media_settings_tag;
  envelope.body.media_settings.duck_mode = mode;
  return envelope;
}

inline Envelope vizSettings(bool enabled, cadence_audiod_v1_VizSource source = cadence_audiod_v1_VizSource_VIZ_SOURCE_MUSIC) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_media_settings_tag;
  auto& settings = envelope.body.media_settings;
  settings.has_viz = true;
  settings.viz.enabled = enabled;
  settings.viz.source = source;
  return envelope;
}

// 20 ms packets of a 16 kHz sine, as the server's reply stream carries them
inline std::vector<Packet> opusTone(std::size_t count, double hz, double amplitude) {
  OpusEncoder encoder;
  REQUIRE(encoder.open().ok());
  std::vector<Packet> packets;
  std::vector<std::int16_t> frame(kEncoderFrameSamples);
  for (std::size_t index = 0; index < count; ++index) {
    for (std::size_t sample = 0; sample < frame.size(); ++sample) {
      const double seconds = static_cast<double>(index * frame.size() + sample) / kOpusSampleRate;
      frame[sample] = static_cast<std::int16_t>(amplitude * std::sin(2 * std::numbers::pi * hz * seconds));
    }
    auto packet = encoder.encode(frame);
    REQUIRE(packet.ok());
    packets.push_back(packet.value());
  }
  return packets;
}

inline void sendPackets(int fd, std::string_view id, const std::vector<Packet>& packets, std::size_t from = 0,
                        std::size_t to = SIZE_MAX) {
  for (std::size_t seq = from; seq < std::min(to, packets.size()); ++seq)
    sendFrame(fd, audioDown(id, static_cast<std::uint32_t>(seq), packets[seq]));
}

inline void playStream(int fd, std::string_view id, const std::vector<Packet>& packets) {
  sendFrame(fd, streamStart(id));
  sendPackets(fd, id, packets);
  sendFrame(fd, streamEnd(id));
}

// the next frame is the stream's StreamDone, which may wait until the stream played out
inline StreamDone expectDone(int fd, std::string_view id) {
  const auto received = receive(fd, std::chrono::seconds(5));
  REQUIRE(received.kind == Received::Kind::frame);
  REQUIRE(received.envelope.which_body == cadence_audiod_v1_Envelope_stream_done_tag);
  const auto done = received.envelope.body.stream_done;
  CHECK(std::string_view(done.stream_id) == id);
  return done;
}

// sends a VolumeSet and returns its answer, which says the set's level, ok and transient back
inline cadence_audiod_v1_VolumeApplied expectApplied(int fd, std::uint32_t level, bool ok, bool transient = false) {
  sendFrame(fd, volumeSet(level, transient));
  const auto applied = expectFrame(fd, cadence_audiod_v1_Envelope_volume_applied_tag).body.volume_applied;
  CHECK(applied.level == level);
  CHECK(applied.ok == ok);
  CHECK(applied.transient == transient);
  return applied;
}

inline UniqueFd connectConfigured(const AudiodHarness& audiod, const std::string& dir, std::string_view mixerControl = {},
                                  std::uint32_t channels = 0) {
  auto client = connectTo(audiod.socket());
  sendFrame(client.get(), configureOutput(dir, mixerControl, channels));
  expectReady(client.get());
  return client;
}

// the player stops once cadenced is gone, which finishes the mix file
inline void disconnect(const AudiodHarness& audiod, UniqueFd& client) {
  const auto closed = audiod.logs().count(event("playback_closed"));
  client.reset();
  REQUIRE(eventually([&] { return audiod.logs().count(event("playback_closed")) > closed; }));
}

inline WavReader readWav(const std::string& path) {
  auto reader = WavReader::open(path);
  REQUIRE(reader.ok());
  return std::move(reader.value());
}

// the first channel's samples
inline std::vector<std::int16_t> firstChannel(const WavReader& wav) {
  std::vector<std::int16_t> samples;
  for (std::size_t index = 0; index < wav.samples().size(); index += wav.format().channels)
    samples.push_back(wav.samples()[index]);
  return samples;
}

inline int peak(const std::vector<std::int16_t>& samples) {
  int highest = 0;
  for (const auto sample : samples)
    highest = std::max(highest, std::abs(static_cast<int>(sample)));
  return highest;
}

inline double rms(const std::vector<std::int16_t>& samples) {
  double sum = 0;
  for (const auto sample : samples)
    sum += static_cast<double>(sample) * sample;
  return samples.empty() ? 0 : std::sqrt(sum / static_cast<double>(samples.size()));
}

// a board's hardware control, shared with the test that scripts it
class FakeVolume {
 public:
  std::optional<VolumeError> setPercent(int percent) {
    std::scoped_lock lock(mutex_);
    levels_.push_back(percent);
    writes_.push_back(std::chrono::steady_clock::now());
    if (writeFailures_ > 0) {
      --writeFailures_;
      return VolumeError{"fake control missing", std::nullopt};
    }
    return failure_;
  }
  // the control holds the last level written unless a read-back failure is scripted
  std::optional<VolumeError> verifyPercent(int percent) {
    std::scoped_lock lock(mutex_);
    ++verifies_;
    if (readFailures_ > 0) {
      --readFailures_;
      return VolumeError{"fake control holds another level", ReadBackMismatch{77, 255, 0}};
    }
    if (readFailure_)
      return readFailure_;
    if (levels_.empty() || levels_.back() != percent)
      return VolumeError{"fake control holds another level", std::nullopt};
    return std::nullopt;
  }
  void fail() {
    std::scoped_lock lock(mutex_);
    failure_ = VolumeError{"fake control refused", std::nullopt};
  }
  void failReadBack() {
    std::scoped_lock lock(mutex_);
    readFailure_ = VolumeError{"fake control unreadable", ReadBackMismatch{5, 9, 1}};
  }
  // the next writes fail as a control that is not there yet does, the ones after hold
  void failWrites(int count) {
    std::scoped_lock lock(mutex_);
    writeFailures_ = count;
  }
  // the next read-backs find another level, as a concurrent writer leaves it
  void failReadBacks(int count) {
    std::scoped_lock lock(mutex_);
    readFailures_ = count;
  }
  // what fail() and failReadBack() broke works again
  void heal() {
    std::scoped_lock lock(mutex_);
    failure_.reset();
    readFailure_.reset();
  }
  [[nodiscard]] int verifies() const {
    std::scoped_lock lock(mutex_);
    return verifies_;
  }
  [[nodiscard]] std::vector<int> levels() const {
    std::scoped_lock lock(mutex_);
    return levels_;
  }
  // when each level was written
  [[nodiscard]] std::vector<std::chrono::steady_clock::time_point> writes() const {
    std::scoped_lock lock(mutex_);
    return writes_;
  }

 private:
  mutable std::mutex mutex_;
  std::vector<int> levels_;
  std::vector<std::chrono::steady_clock::time_point> writes_;
  std::optional<VolumeError> failure_;
  std::optional<VolumeError> readFailure_;
  int writeFailures_ = 0;
  int readFailures_ = 0;
  int verifies_ = 0;
};

class FakeVolumeControl final : public VolumeControl {
 public:
  explicit FakeVolumeControl(std::shared_ptr<FakeVolume> fake) : fake_(std::move(fake)) {}
  std::optional<VolumeError> setPercent(int percent) override { return fake_->setPercent(percent); }
  std::optional<VolumeError> verifyPercent(int percent) override { return fake_->verifyPercent(percent); }

 private:
  std::shared_ptr<FakeVolume> fake_;
};

// the file backend, plus the fake as the hardware control when the configuration names one
inline OutputFactory withFakeControl(std::shared_ptr<FakeVolume> fake) {
  return [fake](const OutputConfig& config) {
    auto opened = audiod::openOutput(config);
    if (opened.ok() && config.mixer_control[0] != '\0')
      opened.value().volume = std::make_unique<FakeVolumeControl>(fake);
    return opened;
  };
}

}  // namespace cadence::test_support

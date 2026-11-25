#pragma once

#include "cadence/audiod/v1/audiod.pb.h"
#include "playback/alsa-player.h"
#include "playback/alsa-playback.h"
#include "playback/volume-control.h"
#include "result.h"

#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

using OutputConfig = cadence_audiod_v1_OutputConfig;

// pickle's playback layout: 20 ms periods into dmix and at most 60 ms waiting in the device. Configure carries
// no layout yet, and the file backend is paced like pickle with it
inline constexpr AlsaPlayerConfig kPlayerConfig{
    .periodFrames = 960, .bufferFrames = 7680, .maxDeviceFillFrames = 2880, .jitter = {}};

// the board's period and buffer replace pickle's when set, the fill cap held to the buffer less a period, never under two periods
void applyLayout(AlsaPlayerConfig& config, const OutputConfig& output);

// what Configure's output section opens
struct Output {
  // the device the player writes
  std::unique_ptr<PcmSink> sink;
  // the board's hardware control; without one the volume is a software gain over the mix
  std::unique_ptr<VolumeControl> volume;
  // the file backend records each stream's decoded audio in this directory
  std::optional<std::string> recordDir;
};

// the error is a reason token, safe to log
using OutputFactory = std::function<Result<Output, std::string_view>(const OutputConfig& config)>;

// alsa: the configured device, with its hardware control when mixer_control names one. file: software volume,
// each stream recorded in dir, and the mix as <dir>/mix/mix-000.wav without its silent stretches (each
// configuration's mix replaces the last)
Result<Output, std::string_view> openOutput(const OutputConfig& config);

}  // namespace cadence::audiod

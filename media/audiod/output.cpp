#include "audiod/output.h"

#include "audiod/system-volume.h"

#include "file/file-output.h"

#ifdef CADENCE_MEDIA_ALSA
#include "playback/alsa-mixer.h"
#endif
#ifdef CADENCE_MEDIA_AAUDIO
#include "aaudio/aaudio-playback.h"
#endif

#include <algorithm>
#include <filesystem>
#include <system_error>

namespace cadence::audiod {

void applyLayout(AlsaPlayerConfig& config, const OutputConfig& output) {
  if (output.period_frames > 0)
    config.periodFrames = output.period_frames;
  if (output.buffer_frames > 0)
    config.bufferFrames = output.buffer_frames;
  // never to the brim: a period of the buffer stays free; a layout the floor cannot fit is left for the open to refuse
  const std::size_t room = config.bufferFrames > config.periodFrames ? config.bufferFrames - config.periodFrames : 0;
  config.maxDeviceFillFrames = std::max<std::size_t>(2 * config.periodFrames, std::min(config.maxDeviceFillFrames, room));
}

namespace {

Result<Output, std::string_view> openFile(const OutputConfig& config) {
  const std::string dir = config.dir;
  if (dir.empty())
    return std::string_view("dir");
  const auto mixDir = std::filesystem::path(dir) / "mix";
  std::error_code error;
  std::filesystem::create_directories(mixDir, error);
  if (error)
    return std::string_view("mkdir");
  Output output;
  output.sink = std::make_unique<FileOutput>(mixDir.string(), "mix", realFileClock(), FileSilence::Drop);
  output.recordDir = dir;
  return output;
}

#ifdef CADENCE_MEDIA_ALSA
Result<Output, std::string_view> openAlsa(const OutputConfig& config) {
  Output output;
  output.sink = std::make_unique<AlsaPlayback>(config.device);
  if (config.mixer_control[0] != '\0')
    output.volume = std::make_unique<AlsaMixer>(config.device, config.mixer_control);
  return output;
}
#endif

}  // namespace

Result<Output, std::string_view> openOutput(const OutputConfig& config) {
  switch (config.backend) {
    case cadence_audiod_v1_Backend_BACKEND_FILE:
      return openFile(config);
#ifdef CADENCE_MEDIA_ALSA
    case cadence_audiod_v1_Backend_BACKEND_ALSA:
      return openAlsa(config);
#endif
#ifdef CADENCE_MEDIA_AAUDIO
    case cadence_audiod_v1_Backend_BACKEND_AAUDIO: {
      // the level is the system's STREAM_MUSIC where the board says so and the app's hook can read it (audiod's own
      // gain then stays at unity), else a software gain over the mix
      Output output;
      output.sink = std::make_unique<AAudioPlayback>(aaudio::ndkApi());
      if (config.system_volume)
        output.volume = SystemVolumeControl::open();
      return output;
    }
#endif
    default:
      return std::string_view("backend");
  }
}

}  // namespace cadence::audiod

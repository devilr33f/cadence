#pragma once

#include "file/file-clock.h"
#include "file/wav.h"
#include "playback/alsa-playback.h"

#include <cstddef>
#include <cstdint>
#include <deque>
#include <optional>
#include <string>
#include <vector>

namespace cadence {

// what a file keeps of the silence the player writes while nothing plays
enum class FileSilence {
  Keep,
  // played stretches that are silent throughout are left out, so a device that runs for days only grows its
  // file while something sounds; the gaps between sounds are lost
  Drop,
};

// a playback device that listens into wav files, one per open (`<prefix>-<nnn>.wav` in dir). it behaves like
// dmix: written frames wait in a device buffer of at most the granted fill and play out at the stream's own
// rate by the clock, so the player is paced like on hardware. a file holds the frames that played; close
// plays out what is left, and only rewind takes frames back. underruns are not reported
class FileOutput final : public PcmSink {
 public:
  FileOutput(std::string dir, std::string prefix = "out", FileClock& clock = realFileClock(),
             FileSilence silence = FileSilence::Keep);
  ~FileOutput() override;

  FileOutput(const FileOutput&) = delete;
  FileOutput& operator=(const FileOutput&) = delete;

  // grants what is asked for; only s16 is written
  Result<PlaybackFormat, PlaybackError> open(const PlaybackFormat& requested) override;
  PlaybackWrite write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) override;
  Result<std::size_t, PlaybackError> rewind() override;
  Result<std::size_t, PlaybackError> delayFrames() override;
  std::optional<PlaybackError> drop() override;
  void close() override;

  // every file opened so far, in order
  [[nodiscard]] const std::vector<std::string>& files() const { return files_; }

 private:
  using Duration = FileClock::TimePoint::duration;

  // moves the frames the clock has played from the device buffer into the file
  void advance();
  [[nodiscard]] std::size_t bufferedFrames() const { return buffer_.size() / channels_; }
  [[nodiscard]] Duration frameTime(std::size_t frames, bool roundUp = false) const;
  std::optional<PlaybackError> flush(std::size_t frames);

  std::string dir_;
  std::string prefix_;
  FileClock& clock_;
  FileSilence silence_;
  std::vector<std::string> files_;

  std::optional<WavWriter> writer_;
  PlaybackFormat format_;
  std::size_t channels_ = 2;
  std::size_t capacityFrames_ = 0;
  std::deque<std::int16_t> buffer_;
  // the stream's own time up to which frames have played
  FileClock::TimePoint played_{};
  // a file write that failed while frames played out; reported by the next write
  std::optional<PlaybackError> fault_;
};

}  // namespace cadence

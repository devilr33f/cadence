#pragma once

#include "playback/volume-control.h"

#include <cstddef>
#include <deque>
#include <functional>
#include <memory>
#include <optional>

extern "C" {

// the host's system volume (android's STREAM_MUSIC, through the app's jni in libcadence-daemon.so). read gives the
// stream's index, its top index and its mute and answers 0, or a negative errno when there is none to read (a
// fixed-volume stream: hdmi-cec passthrough); write sets an index and answers 0 or a negative errno. both run on
// audiod's loop thread
typedef struct cadence_system_volume_backend {
  void* ctx;
  int (*read)(void* ctx, int* index, int* max, int* muted);
  int (*write)(void* ctx, int index);
} cadence_system_volume_backend_t;

// the host sets it before audiod's first Configure (null takes it away); it outlives every audiod. an output whose
// Configure says system_volume uses it, else the level stays a software gain
void cadence_audiod_system_volume(const cadence_system_volume_backend_t* backend);

// the host heard the system volume change (its volume broadcast, an observer): any thread, any time, as often as it
// likes; audiod reads the stream again on its own thread
void cadence_audiod_system_volume_changed(void);
}

namespace cadence::audiod {

// one read of the stream
struct SystemReading {
  int index = 0;
  int max = 0;
  bool muted = false;
};

// the system volume as cadenced hears it: the level 0-100 and the stream's mute
struct SystemChange {
  int level = 0;
  bool muted = false;

  bool operator==(const SystemChange&) const = default;
};

// the level (0-100) and the stream's index (0..max) kept in step, without a jump either way: the index cadenced's
// level maps to is the nearest one, an index the system moved to maps to the nearest level, and an index that is the
// level in force's own maps back to that level (so 30 on a stream of 15 stays 30, never 33). a level the system moved
// to is reported to cadenced, which stores it and sends it back as a VolumeSet; that echo writes nothing, so the
// system is never pulled back to a level it already moved on from (the remote's key held down)
class SystemLevels {
 public:
  static int indexOf(int level, int max);
  static int levelOf(int index, int max);

  // the reading at Configure: the level the stream holds, for Ready
  SystemChange start(const SystemReading& r);
  // a VolumeSet's level against what the stream holds now: the index to write, or none when nothing is to be
  // written (the stream holds the level's index, or the level is an echo of a move audiod reported)
  std::optional<int> set(int level, const SystemReading& now);
  // whether the stream now holds what set(level) wanted: an echo holds whatever the system moved on to
  [[nodiscard]] bool holds(int level, const SystemReading& now) const;
  // a reading after the host said the volume changed: the change to report, or none when the stream holds what
  // audiod set or last reported
  std::optional<SystemChange> observe(const SystemReading& r);

  [[nodiscard]] SystemChange current() const { return {level_, muted_}; }

 private:
  static constexpr std::size_t kMaxEchoes = 16;

  int index_ = -1;
  int level_ = 0;
  bool muted_ = false;
  // the levels reported to cadenced, oldest first, whose VolumeSet has not come back yet
  std::deque<int> echoes_;
  // the last set was an echo
  bool echoed_ = false;
};

// audiod's VolumeControl over the host's system volume: the level is the stream's, and audiod's own gain stays at
// unity, so the mix leaves audiod at full resolution and the system's mixer applies the one attenuation
class SystemVolumeControl final : public VolumeControl {
 public:
  // null when the output may not use one: no backend set, or a stream it cannot read (a fixed volume, max 0)
  static std::unique_ptr<SystemVolumeControl> open();

  [[nodiscard]] std::optional<VolumeError> setPercent(int percent) override;
  [[nodiscard]] std::optional<VolumeError> verifyPercent(int percent) override;

  // after the host said the volume changed: the change to report to cadenced, if the system moved by itself
  std::optional<SystemChange> poll();
  [[nodiscard]] SystemChange current() const { return levels_.current(); }

  // a backend and its first reading (open's, and the tests')
  SystemVolumeControl(const cadence_system_volume_backend_t& backend, const SystemReading& first);

 private:
  std::optional<SystemReading> read() const;

  cadence_system_volume_backend_t backend_;
  SystemLevels levels_;
};

// the backend in force, null when the host set none
const cadence_system_volume_backend_t* systemVolumeBackend();
// who hears the host's change notices: one listener at a time (the playback whose output holds the control), from
// any thread; an empty function stops it
void onSystemVolumeChanged(std::function<void()> listener);

}  // namespace cadence::audiod

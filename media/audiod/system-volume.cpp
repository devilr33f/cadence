#include "audiod/system-volume.h"

#include <algorithm>
#include <atomic>
#include <mutex>
#include <utility>

namespace cadence::audiod {

namespace {

std::atomic<const cadence_system_volume_backend_t*> backendInForce{nullptr};

std::mutex listenerMu;
std::function<void()> listener;

}  // namespace

const cadence_system_volume_backend_t* systemVolumeBackend() { return backendInForce.load(); }

void onSystemVolumeChanged(std::function<void()> fn) {
  const std::lock_guard lock(listenerMu);
  listener = std::move(fn);
}

int SystemLevels::indexOf(int level, int max) {
  if (max <= 0)
    return 0;
  level = std::clamp(level, 0, 100);
  return (level * max + 50) / 100;
}

// for a stream of at most 100 steps every index has a level of its own, and indexOf takes it back to that index
int SystemLevels::levelOf(int index, int max) {
  if (max <= 0)
    return 0;
  index = std::clamp(index, 0, max);
  return (index * 100 + max / 2) / max;
}

SystemChange SystemLevels::start(const SystemReading& r) {
  index_ = r.index;
  level_ = levelOf(r.index, r.max);
  muted_ = r.muted;
  echoes_.clear();
  echoed_ = false;
  return current();
}

std::optional<int> SystemLevels::set(int level, const SystemReading& now) {
  echoed_ = false;
  if (const auto it = std::find(echoes_.begin(), echoes_.end(), level); it != echoes_.end()) {
    // cadenced followed a move audiod reported: the system is there or has moved on, so nothing is written back
    echoes_.erase(echoes_.begin(), it + 1);
    echoed_ = true;
    return std::nullopt;
  }
  // cadenced's own level (a voice turn, the dashboard, a sender): it wins over any move still on its way
  echoes_.clear();
  const int want = indexOf(level, now.max);
  index_ = want;
  level_ = std::clamp(level, 0, 100);
  if (now.index == want)
    return std::nullopt;
  return want;
}

bool SystemLevels::holds(int level, const SystemReading& now) const {
  return echoed_ || now.index == indexOf(level, now.max);
}

std::optional<SystemChange> SystemLevels::observe(const SystemReading& r) {
  if (r.max <= 0)
    return std::nullopt;
  const bool moved = r.index != index_;
  if (!moved && r.muted == muted_)
    return std::nullopt;
  if (moved) {
    index_ = r.index;
    level_ = levelOf(r.index, r.max);
    echoes_.push_back(level_);
    if (echoes_.size() > kMaxEchoes)
      echoes_.pop_front();
  }
  muted_ = r.muted;
  return current();
}

std::unique_ptr<SystemVolumeControl> SystemVolumeControl::open() {
  const auto* backend = systemVolumeBackend();
  if (backend == nullptr || backend->read == nullptr || backend->write == nullptr)
    return nullptr;
  int index = 0;
  int max = 0;
  int muted = 0;
  if (backend->read(backend->ctx, &index, &max, &muted) != 0 || max <= 0 || max > 100 || index < 0 || index > max)
    return nullptr;
  return std::make_unique<SystemVolumeControl>(*backend, SystemReading{index, max, muted != 0});
}

SystemVolumeControl::SystemVolumeControl(const cadence_system_volume_backend_t& backend, const SystemReading& first)
    : backend_(backend) {
  levels_.start(first);
}

std::optional<SystemReading> SystemVolumeControl::read() const {
  int index = 0;
  int max = 0;
  int muted = 0;
  if (backend_.read(backend_.ctx, &index, &max, &muted) != 0 || max <= 0 || index < 0 || index > max)
    return std::nullopt;
  return SystemReading{index, max, muted != 0};
}

std::optional<VolumeError> SystemVolumeControl::setPercent(int percent) {
  const auto now = read();
  if (!now)
    return VolumeError{"system_read", std::nullopt};
  if (const auto index = levels_.set(percent, *now)) {
    if (backend_.write(backend_.ctx, *index) != 0)
      return VolumeError{"system_write", std::nullopt};
  }
  return std::nullopt;
}

std::optional<VolumeError> SystemVolumeControl::verifyPercent(int percent) {
  const auto now = read();
  if (!now)
    return VolumeError{"system_read", std::nullopt};
  if (levels_.holds(percent, *now))
    return std::nullopt;
  return VolumeError{"system_readback", ReadBackMismatch{SystemLevels::indexOf(percent, now->max), now->index, 0}};
}

std::optional<SystemChange> SystemVolumeControl::poll() {
  const auto now = read();
  if (!now)
    return std::nullopt;
  return levels_.observe(*now);
}

}  // namespace cadence::audiod

extern "C" void cadence_audiod_system_volume(const cadence_system_volume_backend_t* backend) {
  cadence::audiod::backendInForce.store(backend);
}

extern "C" void cadence_audiod_system_volume_changed(void) {
  std::function<void()> fn;
  {
    const std::lock_guard lock(cadence::audiod::listenerMu);
    fn = cadence::audiod::listener;
  }
  if (fn)
    fn();
}

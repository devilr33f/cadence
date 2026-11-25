// the client's sink input as a server would report it: event subscription, its stored volume and its info
#include "pulse-shim/introspect.h"

#include "pulse-shim/context.h"
#include "pulse-shim/guard.h"

#include <algorithm>
#include <cstdint>
#include <tuple>

namespace cadence::pulse {

namespace {

// the stored volume over the stream's channels: their average when the counts differ
pa_cvolume remap(const pa_cvolume& volume, std::uint8_t channels) {
  if (volume.channels == channels)
    return volume;
  std::uint64_t sum = 0;
  for (std::size_t i = 0; i < volume.channels; ++i)
    sum += volume.values[i];
  const auto average = static_cast<pa_volume_t>(sum / std::max<std::uint64_t>(volume.channels, 1));
  pa_cvolume remapped{};
  remapped.channels = channels;
  std::fill_n(remapped.values, channels, average);
  return remapped;
}

// the sink input is one or two channels, as the stream is; its volume is a copy, valid values only
std::optional<pa_cvolume> sinkInputVolume(const pa_cvolume* volume) {
  if (volume == nullptr || volume->channels < 1 || volume->channels > 2)
    return std::nullopt;
  pa_cvolume copy{};
  copy.channels = volume->channels;
  for (std::size_t i = 0; i < volume->channels; ++i) {
    if (volume->values[i] > PA_VOLUME_MAX)
      return std::nullopt;
    copy.values[i] = volume->values[i];
  }
  return copy;
}

// the documented subscription rule decides whether the subscriber hears the event
void notify(Context& context, pa_subscription_event_type_t event, std::uint32_t index) {
  const auto [mask, cb, userdata] = context.withSinkInput(
      [](const SinkInput& sinkInput) { return std::tuple{sinkInput.mask, sinkInput.onEvent, sinkInput.eventData}; });
  const auto facility = 1U << (event & PA_SUBSCRIPTION_EVENT_FACILITY_MASK);
  if (cb != nullptr && (mask & facility) != 0)
    cb(context.raw(), event, index, userdata);
}

bool sameVolume(const pa_cvolume& a, const pa_cvolume& b) {
  return a.channels == b.channels && std::equal(a.values, a.values + a.channels, b.values);
}

}  // namespace

pa_cvolume SinkInput::stereoNorm() {
  pa_cvolume volume{};
  volume.channels = 2;
  volume.values[0] = volume.values[1] = PA_VOLUME_NORM;
  return volume;
}

bool SinkInput::store(const pa_cvolume& stored) {
  const bool changed = !sameVolume(volume, stored);
  volume = stored;
  return changed;
}

pa_sink_input_info SinkInput::info(std::uint32_t index, std::optional<pa_sample_spec> playing) const {
  const auto spec = playing ? *playing : pa_sample_spec{PA_SAMPLE_FLOAT32LE, 44100, volume.channels};
  pa_sink_input_info info{};
  info.index = index;
  info.name = "cadence";
  info.owner_module = PA_INVALID_INDEX;
  info.client = PA_INVALID_INDEX;
  info.sink = 0;
  info.sample_spec = spec;
  info.channel_map.channels = spec.channels;
  std::fill_n(info.channel_map.map, PA_CHANNELS_MAX, PA_CHANNEL_POSITION_INVALID);
  if (spec.channels == 1) {
    info.channel_map.map[0] = PA_CHANNEL_POSITION_MONO;
  } else {
    info.channel_map.map[0] = PA_CHANNEL_POSITION_FRONT_LEFT;
    info.channel_map.map[1] = PA_CHANNEL_POSITION_FRONT_RIGHT;
  }
  info.volume = remap(volume, spec.channels);
  info.resample_method = "";
  info.driver = "cadence";
  info.has_volume = 1;
  info.volume_writable = 1;
  return info;
}

}  // namespace cadence::pulse

using namespace cadence::pulse;

extern "C" {

void pa_context_set_subscribe_callback(pa_context* c, pa_context_subscribe_cb_t cb, void* userdata) {
  guarded([&] {
    if (const auto context = Context::retain(c))
      context->withSinkInput([&](SinkInput& sinkInput) {
        sinkInput.onEvent = cb;
        sinkInput.eventData = userdata;
      });
  });
}

pa_operation* pa_context_subscribe(pa_context* c, pa_subscription_mask_t m, pa_context_success_cb_t cb,
                                   void* userdata) {
  return guarded(static_cast<pa_operation*>(nullptr), [&]() -> pa_operation* {
    const auto context = Context::retain(c);
    if (!context)
      return nullptr;
    return context->operation([m, cb, userdata](Context& subscribed) {
      subscribed.withSinkInput([m](SinkInput& sinkInput) { sinkInput.mask = m; });
      subscribed.succeed(cb, userdata);
    });
  });
}

// stored and reported back, never applied: the server plays at unity gain
pa_operation* pa_context_set_sink_input_volume(pa_context* c, std::uint32_t idx, const pa_cvolume* volume,
                                               pa_context_success_cb_t cb, void* userdata) {
  return guarded(static_cast<pa_operation*>(nullptr), [&]() -> pa_operation* {
    const auto context = Context::retain(c);
    if (!context)
      return nullptr;
    const auto stored = sinkInputVolume(volume);
    if (!stored) {
      context->setError(PA_ERR_INVALID);
      return nullptr;
    }
    return context->operation([idx, stored = *stored, cb, userdata](Context& changed) {
      const bool moved = changed.withSinkInput([&](SinkInput& sinkInput) { return sinkInput.store(stored); });
      changed.succeed(cb, userdata);
      if (moved)
        notify(changed, PA_SUBSCRIPTION_EVENT_SINK_INPUT | PA_SUBSCRIPTION_EVENT_CHANGE, idx);
    });
  });
}

pa_operation* pa_context_get_sink_input_info(pa_context* c, std::uint32_t idx, pa_sink_input_info_cb_t cb,
                                             void* userdata) {
  return guarded(static_cast<pa_operation*>(nullptr), [&]() -> pa_operation* {
    const auto context = Context::retain(c);
    if (!context)
      return nullptr;
    return context->operation([idx, cb, userdata](Context& asked) {
      if (cb == nullptr)
        return;
      const auto playing = asked.playing();
      const auto info = asked.withSinkInput([&](const SinkInput& sinkInput) { return sinkInput.info(idx, playing); });
      cb(asked.raw(), &info, 0, userdata);
      cb(asked.raw(), nullptr, 1, userdata);
    });
  });
}

}  // extern "C"

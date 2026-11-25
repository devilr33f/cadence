#include "player/audio-route.h"

namespace cadence::screen::player {

AudioRouteInfo audioRouteFor(AoMode mode, cadence_screen_v1_MediaPurpose purpose) {
  AudioRouteInfo info;
  if (purpose != cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT) {
    info.route = cadence_v2_AudioRoute_AUDIO_ROUTE_NONE;
    info.silent = true;
    return info;
  }
  if (mode == AoMode::Cadence) {
    info.route = cadence_v2_AudioRoute_AUDIO_ROUTE_MIXER;
    info.controls = {cadence_v2_AudioControl_AUDIO_CONTROL_VOLUME, cadence_v2_AudioControl_AUDIO_CONTROL_DUCK,
                     cadence_v2_AudioControl_AUDIO_CONTROL_MUTE,   cadence_v2_AudioControl_AUDIO_CONTROL_EQ,
                     cadence_v2_AudioControl_AUDIO_CONTROL_VIZ_PCM, cadence_v2_AudioControl_AUDIO_CONTROL_VIZ_BANDS};
    // audiod's mixer applies a control at its next period, ramps aside
    info.controlLatencyMs = 10;
    return info;
  }
  info.route = cadence_v2_AudioRoute_AUDIO_ROUTE_DIRECT;
  info.controls = {cadence_v2_AudioControl_AUDIO_CONTROL_VOLUME, cadence_v2_AudioControl_AUDIO_CONTROL_DUCK,
                   cadence_v2_AudioControl_AUDIO_CONTROL_MUTE, cadence_v2_AudioControl_AUDIO_CONTROL_EQ};
  // mpv's volume reaches the platform's buffer: audiotrack on android sits at 100-300 ms (plan item 19)
  info.controlLatencyMs = 200;
  info.playerControls = true;
  return info;
}

AoMode buildAoMode() {
#if defined(CADENCE_SCREEN_AO_CADENCE)
  return AoMode::Cadence;
#else
  return AoMode::Stock;
#endif
}

}  // namespace cadence::screen::player

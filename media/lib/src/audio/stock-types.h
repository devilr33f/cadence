#pragma once

#include <cstddef>

// c abi of the vqe and spotter engines in a board's stock vendor library, named by Configure
namespace cadence::stock_abi {

struct VqeConfig;
struct Vqe;
struct VqeError;
struct SpotterConf;
struct Spotter;
struct MultiConf;
struct Component;

// vqe calls return an owned error, or null on success
using VqeConfigCreateFromPreset = VqeError*(const char* preset, VqeConfig** config);
using VqeConfigFree = void(VqeConfig* config);
using VqeConfigGetInt = int(VqeConfig* config);
// the size/pointer pair is an optional extension list; it is always empty here
using VqeCreate = VqeError*(VqeConfig* config, const char* modelDir, std::size_t, const void*, Vqe** vqe);
using VqeFree = void(Vqe* vqe);
// buffers are frame-interleaved floats in [-1, 1]; counts are samples, not frames
using VqeProcess = VqeError*(Vqe* vqe, const float* mics, std::size_t micSamples, const float* speakers,
                             std::size_t speakerSamples);
// data stays owned by the engine and is valid until the next process call
using VqeGetOutput = VqeError*(Vqe* vqe, int channelType, std::size_t channelIndex, const float** data,
                               std::size_t* samples);
using VqeErrorMessage = const char*(const VqeError* error);
using VqeErrorFree = void(VqeError* error);

// pickle firmware spotter api: failures come back as a malloc'd message in a trailing char**,
// which the caller releases with the library's free() even when the call succeeded
using SpotterConfRead = SpotterConf*(const char* modelDir, char** message);
using SpotterConfFree = void(SpotterConf* conf);
using SpotterConfNativeRate = int(SpotterConf* conf);
using SpotterConfChannelCount = int(SpotterConf* conf, int channelType);
// the reserved pointer is always null and the flag always false, as the stock daemon passes them
using SpotterCreateMultichannel = Spotter*(SpotterConf* conf, int sampleRate, int channelCount, const int* channelTypes,
                                           char** message, const void* reserved, bool flag);
using SpotterDestroy = void(Spotter* spotter);
// s16 samples interleaved in the channel order given at creation. unlike the other calls it takes no
// message slot: the stock daemon passes only these three arguments and ignores the return value
using SpotterSubmit = void(Spotter* spotter, const void* data, std::size_t bytes);
using SpotterNextEvent = int(Spotter* spotter);
using MessageFree = void(void* message);

// the combined spotter stock maind builds: confs attached to one multi conf as components. a configuration
// error inside attach or create logs and quick_exit(1)s the whole process instead of returning.
// the preset only matters with localCa, the counteractivation score, which stays off
using SpotterMultiConfCreate = MultiConf*(const char* preset, bool localCa);
// takes ownership of conf; the component handle stays owned by the multi conf. the message slot is never written
using SpotterMultiConfAttach = Component*(MultiConf* multi, SpotterConf* conf, char** message);
using SpotterMultiConfDetach = void(MultiConf* multi, Component* component);
using SpotterMultiConfFree = void(MultiConf* multi);
// the message slot and reserved pointer are unused. threaded gives each component its own worker, and
// minChunkBytes holds events back until that much audio per channel was submitted
using SpotterCreateFromMultiConf = Spotter*(MultiConf* multi, int rate, int count, const int* types, char** message,
                                            void* reserved, bool threaded, unsigned minChunkBytes);
// the component of the latest hit, as attach returned it
using SpotterCurrentComponent = Component*(Spotter* spotter);
// one shared library buffer that later calls rewrite: copy it at once, never free it. without a current
// phrase (phrase 0) the library asserts and aborts
using SpotterCurrentPhrase = const char*(Spotter* spotter);

// voicetech AudioInputChannelType values selecting vqe outputs
enum class VqeOutput : int {
  RawMainMicSync = 2,
  RawAuxiliaryMicSync = 3,
  RawFeedbackSync = 5,
  Omni = 6,
  Beamforming = 7,
  BackgroundNoiseReducer = 8,
  OmniWithGain = 9,
};

// spotter channel types are 0..5, as the model flags declare them: 0 omni, 1 beamforming, 2 feedback,
// 3 bnr, 4 raw mic, 5 extra raw mics
inline constexpr int kSpotterChannelTypeCount = 6;

// spotter_get_next_event codes are 0..9
inline constexpr int kSpotterMaxEvent = 9;
inline constexpr int kSpotterFirstConfirmation = 2;
inline constexpr int kSpotterSecondConfirmation = 5;

constexpr bool endsSpotterEvents(int event) { return event == 0 || event == 4 || event == 7 || event == 9; }

}  // namespace cadence::stock_abi

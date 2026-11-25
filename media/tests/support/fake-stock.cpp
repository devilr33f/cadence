#include "support/fake-stock.h"

#include "audio-layout.h"
#include "support/test-layout.h"

#include <algorithm>
#include <cstring>
#include <utility>

namespace cadence::fake_stock {

namespace {

FakeStock* fake = nullptr;

template <typename Fn>
std::pair<const std::string, void*> entry(const char* name, Fn* function) {
  return {name, reinterpret_cast<void*>(function)};
}

FakeConf& confOf(abi::SpotterConf* conf) { return *reinterpret_cast<FakeConf*>(conf); }

const char* nameOf(const FakeConf& conf) { return conf.command ? "command" : "alice"; }

std::size_t componentIndex(const FakeConf& conf) { return conf.command ? 1 : 0; }

void violate(std::string what) { fake->violations.push_back(std::move(what)); }

bool isMultiSpotter(const abi::Spotter* spotter) {
  return spotter == reinterpret_cast<const abi::Spotter*>(&fake->multiSpotterHandle);
}

int nextEvent() {
  ++fake->nextEventCalls;
  if (fake->events.empty())
    return fake->endlessEvent.value_or(0);
  const int event = fake->events.front();
  fake->events.pop_front();
  return event;
}

const std::map<std::string, void*>& fakeSymbols() {
  static const std::map<std::string, void*> symbols = {
      entry<abi::VqeConfigCreateFromPreset>("YandexVqeC_Config_CreateFromPreset",
                                            [](const char* preset, abi::VqeConfig** config) -> abi::VqeError* {
                                              ++fake->configCreates;
                                              fake->preset = preset;
                                              if (fake->onConfigCreate)
                                                fake->onConfigCreate();
                                              if (auto* error = fake->fail("config"))
                                                return error;
                                              *config = reinterpret_cast<abi::VqeConfig*>(&fake->configHandle);
                                              return nullptr;
                                            }),
      entry<abi::VqeConfigFree>("YandexVqeC_Config_Free", [](abi::VqeConfig*) { ++fake->configFrees; }),
      entry<abi::VqeConfigGetInt>("YandexVqeC_Config_GetInputChunkSize", [](abi::VqeConfig*) { return fake->chunkFrames; }),
      entry<abi::VqeConfigGetInt>("YandexVqeC_Config_GetProcessingFrame", [](abi::VqeConfig*) { return fake->outputFrames; }),
      entry<abi::VqeConfigGetInt>("YandexVqeC_Config_GetInputSamplingRate", [](abi::VqeConfig*) { return fake->inputRate; }),
      entry<abi::VqeConfigGetInt>("YandexVqeC_Config_GetOutputSamplingRate", [](abi::VqeConfig*) { return fake->outputRate; }),
      entry<abi::VqeConfigGetInt>("YandexVqeC_Config_GetMicsCount", [](abi::VqeConfig*) { return fake->mics; }),
      entry<abi::VqeConfigGetInt>("YandexVqeC_Config_GetSpeakersCount", [](abi::VqeConfig*) { return fake->speakers; }),
      entry<abi::VqeCreate>("YandexVqeC_Create",
                            [](abi::VqeConfig*, const char* modelDir, std::size_t, const void*, abi::Vqe** vqe) -> abi::VqeError* {
                              fake->vqeModels = modelDir;
                              if (auto* error = fake->fail("create"))
                                return error;
                              *vqe = reinterpret_cast<abi::Vqe*>(&fake->vqeHandle);
                              return nullptr;
                            }),
      entry<abi::VqeFree>("YandexVqeC_Free", [](abi::Vqe*) { ++fake->vqeFrees; }),
      entry<abi::VqeProcess>("YandexVqeC_Process",
                             [](abi::Vqe*, const float* mics, std::size_t micSamples, const float* speakers,
                                std::size_t speakerSamples) -> abi::VqeError* {
                               ++fake->processCalls;
                               fake->lastMics.assign(mics, mics + micSamples);
                               fake->lastSpeakers.assign(speakers, speakers + speakerSamples);
                               if (fake->onProcess)
                                 fake->onProcess();
                               return fake->fail("process");
                             }),
      entry<abi::VqeGetOutput>("YandexVqeC_GetOutputChannelData",
                               [](abi::Vqe*, int type, std::size_t, const float** data, std::size_t* length) -> abi::VqeError* {
                                 if (auto* error = fake->fail("output"))
                                   return error;
                                 const auto output = fake->outputs.find(type);
                                 if (output == fake->outputs.end()) {
                                   fake->failures["missing-output"] = "no such output";
                                   return fake->fail("missing-output");
                                 }
                                 *data = output->second.data();
                                 *length = output->second.size();
                                 return nullptr;
                               }),
      entry<abi::VqeErrorMessage>("YandexVqeC_Error_GetMessage", [](const abi::VqeError* error) {
        return reinterpret_cast<const FakeError*>(error)->message.c_str();
      }),
      entry<abi::VqeErrorFree>("YandexVqeC_Error_Free", [](abi::VqeError* error) { ++fake->frees[error]; }),
      entry<abi::SpotterConfRead>("spotter_conf_read", [](const char* dir, char** message) -> abi::SpotterConf* {
        fake->spotterModel = dir;
        fake->confReads.emplace_back(dir);
        const bool command = std::filesystem::path(dir) == fake->commandModel;
        if ((*message = fake->failMessage(command ? "command-conf-read" : "conf-read")))
          return nullptr;
        *message = fake->failMessage("conf-read-note");
        fake->confs.push_back(std::make_unique<FakeConf>(FakeConf{command, FakeConf::State::Live}));
        return reinterpret_cast<abi::SpotterConf*>(fake->confs.back().get());
      }),
      entry<abi::SpotterConfFree>("spotter_conf_free",
                                  [](abi::SpotterConf* raw) {
                                    auto& conf = confOf(raw);
                                    ++fake->spotterConfFrees;
                                    if (conf.state != FakeConf::State::Live)
                                      violate(std::string("spotter_conf_free on a conf it does not own: ") + nameOf(conf));
                                    conf.state = FakeConf::State::Freed;
                                  }),
      entry<abi::SpotterConfNativeRate>("spotter_conf_native_sample_rate", [](abi::SpotterConf* conf) {
        return confOf(conf).command ? fake->commandNativeRate : fake->spotterNativeRate;
      }),
      entry<abi::SpotterConfChannelCount>("spotter_conf_get_required_channel_count", [](abi::SpotterConf* conf, int type) {
        const auto& channels = confOf(conf).command ? fake->commandChannels : fake->spotterChannels;
        return channels.at(static_cast<std::size_t>(type));
      }),
      entry<abi::SpotterCreateMultichannel>(
          "spotter_create_multichannel",
          [](abi::SpotterConf*, int sampleRate, int channelCount, const int* types, char** message, const void* reserved,
             bool flag) -> abi::Spotter* {
            fake->spotterCreateRate = sampleRate;
            fake->spotterCreateTypes.assign(types, types + channelCount);
            fake->spotterCreateReserved = reserved;
            fake->spotterCreateFlag = flag;
            if ((*message = fake->failMessage("spotter-create")))
              return nullptr;
            return reinterpret_cast<abi::Spotter*>(&fake->spotterHandle);
          }),
      entry<abi::SpotterDestroy>("spotter_destroy",
                                 [](abi::Spotter* spotter) {
                                   fake->clobberPhrase();
                                   ++fake->spotterDestroys;
                                   if (isMultiSpotter(spotter)) {
                                     fake->multiCalls.emplace_back("destroy");
                                     fake->multiSpotterAlive = false;
                                   }
                                 }),
      entry<abi::SpotterSubmit>("spotter_submit_raw_data",
                                [](abi::Spotter* spotter, const void* data, std::size_t bytes) {
                                  fake->clobberPhrase();
                                  const auto* samples = static_cast<const std::int16_t*>(data);
                                  fake->submitted.assign(samples, samples + bytes / sizeof(std::int16_t));
                                  fake->submittedTo = spotter;
                                }),
      entry<abi::SpotterNextEvent>("spotter_get_next_event", [](abi::Spotter*) -> int {
        fake->clobberPhrase();
        const int event = nextEvent();
        if (event == abi::kSpotterFirstConfirmation || event == abi::kSpotterSecondConfirmation) {
          fake->currentHit = FakeHit{};
          if (!fake->hits.empty()) {
            fake->currentHit = std::move(fake->hits.front());
            fake->hits.pop_front();
          }
        } else {
          fake->currentHit.reset();
        }
        return event;
      }),
      entry<int(abi::Spotter*)>("spotter_get_unhandled_nbytes", [](abi::Spotter*) {
        violate("spotter_get_unhandled_nbytes asserts in the device copy unless the spotter holds a result");
        return 0;
      }),
      entry<abi::MessageFree>("free",
                              [](void* message) {
                                if (message == fake->phraseBuffer.data())
                                  violate("free of the library's phrase buffer");
                                ++fake->frees[message];
                              }),
      entry<abi::SpotterMultiConfCreate>("spotter_multi_conf_create",
                                         [](const char* preset, bool localCa) -> abi::MultiConf* {
                                           fake->multiCalls.emplace_back("create");
                                           fake->multiPresetNull = preset == nullptr;
                                           fake->multiLocalCa = localCa;
                                           if (fake->failures.count("multi-conf-create") != 0)
                                             return nullptr;
                                           fake->multiConfAlive = true;
                                           return reinterpret_cast<abi::MultiConf*>(&fake->multiConfHandle);
                                         }),
      entry<abi::SpotterMultiConfAttach>(
          "spotter_multi_conf_attach_spotter_conf",
          [](abi::MultiConf*, abi::SpotterConf* raw, char** message) -> abi::Component* {
            auto& conf = confOf(raw);
            fake->multiCalls.push_back(std::string("attach ") + nameOf(conf));
            if (!fake->multiConfAlive)
              violate("attach without a multi conf");
            if (conf.state != FakeConf::State::Live)
              violate(std::string("attach of a conf the caller does not own: ") + nameOf(conf));
            // the conf is the library's from here on, even when it refuses it
            if (char* refusal = fake->failMessage(std::string("attach-") + nameOf(conf))) {
              conf.state = FakeConf::State::Kept;
              if (message != nullptr)
                *message = refusal;
              return nullptr;
            }
            conf.state = FakeConf::State::Attached;
            const auto index = componentIndex(conf);
            fake->components[index] = &conf;
            return reinterpret_cast<abi::Component*>(&fake->componentHandles[index]);
          }),
      entry<abi::SpotterMultiConfDetach>(
          "spotter_multi_conf_detach_component", [](abi::MultiConf*, abi::Component* component) {
            const auto* handle = reinterpret_cast<const char*>(component);
            const auto found = std::find_if(fake->componentHandles.begin(), fake->componentHandles.end(),
                                            [handle](const char& slot) { return &slot == handle; });
            if (found == fake->componentHandles.end()) {
              violate("detach of a component never attached");
              return;
            }
            const auto index = static_cast<std::size_t>(found - fake->componentHandles.begin());
            auto* conf = fake->components[index];
            fake->multiCalls.push_back(std::string("detach ") + (index == 1 ? "command" : "alice"));
            if (fake->multiSpotterAlive)
              violate("detach while the spotter built from the multi conf lives");
            if (conf == nullptr) {
              violate("detach of a component already detached");
              return;
            }
            conf->state = FakeConf::State::Freed;
            fake->components[index] = nullptr;
          }),
      entry<abi::SpotterMultiConfFree>("spotter_multi_conf_free", [](abi::MultiConf*) {
        fake->multiCalls.emplace_back("free");
        if (!fake->multiConfAlive)
          violate("multi conf freed while none is alive");
        if (fake->multiSpotterAlive)
          violate("multi conf freed while the spotter built from it lives");
        for (auto*& conf : fake->components) {
          if (conf == nullptr)
            continue;
          violate(std::string("multi conf freed before detaching ") + nameOf(*conf));
          conf->state = FakeConf::State::Freed;
          conf = nullptr;
        }
        fake->multiConfAlive = false;
      }),
      entry<abi::SpotterCreateFromMultiConf>(
          "spotter_create_from_multi_conf",
          [](abi::MultiConf*, int rate, int count, const int* types, char** message, void* reserved, bool threaded,
             unsigned minChunk) -> abi::Spotter* {
            fake->multiCalls.emplace_back("spotter");
            fake->multiCreateRate = rate;
            fake->multiCreateTypes.assign(types, types + count);
            fake->multiCreateMessageNull = message == nullptr;
            fake->multiCreateReservedNull = reserved == nullptr;
            fake->multiCreateThreaded = threaded;
            fake->multiCreateMinChunk = minChunk;
            // the device copy quick_exits on a component whose rate differs or channels it lacks
            for (const auto* conf : fake->components) {
              if (conf == nullptr)
                continue;
              const int own = conf->command ? fake->commandNativeRate : fake->spotterNativeRate;
              if (own != rate)
                violate(std::string("quick_exit: ") + nameOf(*conf) + " rate differs");
              const auto& channels = conf->command ? fake->commandChannels : fake->spotterChannels;
              for (std::size_t type = 0; type < channels.size(); ++type) {
                const auto given = std::count(types, types + count, static_cast<int>(type));
                if (channels[type] < 0 || channels[type] > given)
                  violate(std::string("quick_exit: ") + nameOf(*conf) + " lacks channel type " + std::to_string(type));
              }
            }
            if (fake->failures.count("multi-spotter-create") != 0)
              return nullptr;
            fake->multiSpotterAlive = true;
            return reinterpret_cast<abi::Spotter*>(&fake->multiSpotterHandle);
          }),
      entry<abi::SpotterCurrentComponent>("spotter_get_current_component_id", [](abi::Spotter* spotter) -> abi::Component* {
        fake->clobberPhrase();
        ++fake->componentCalls;
        if (!isMultiSpotter(spotter)) {
          violate("component asked of a single spotter");
          return nullptr;
        }
        if (!fake->currentHit)
          return nullptr;
        switch (fake->currentHit->component) {
          case FakeComponent::Alice:
            return reinterpret_cast<abi::Component*>(&fake->componentHandles[0]);
          case FakeComponent::Command:
            return reinterpret_cast<abi::Component*>(&fake->componentHandles[1]);
          case FakeComponent::Stray:
            return reinterpret_cast<abi::Component*>(&fake->strayComponent);
        }
        return nullptr;
      }),
      entry<abi::SpotterCurrentPhrase>("spotter_get_current_phrase", [](abi::Spotter*) -> const char* {
        ++fake->phraseCalls;
        std::string phrase;
        if (!fake->currentHit)
          violate("asked for phrase 0: no hit");
        else if (fake->currentHit->component != FakeComponent::Command)
          violate("asked for the phrase of a hit that is no command");
        else
          phrase = fake->currentHit->phrase;
        fake->phraseBuffer.fill('\0');
        std::memcpy(fake->phraseBuffer.data(), phrase.data(), std::min(phrase.size(), fake->phraseBuffer.size() - 1));
        return fake->phraseBuffer.data();
      }),
  };
  return symbols;
}

class FakeLibrary final : public StockLibrary {
 public:
  void* symbol(const char* name) const override {
    if (fake->missingSymbols.count(name) != 0)
      return nullptr;
    const auto found = fakeSymbols().find(name);
    return found == fakeSymbols().end() ? nullptr : found->second;
  }
};

}  // namespace

void FakeStock::clobberPhrase() {
  static constexpr char kRewritten[] = "rewritten by a later call";
  phraseBuffer.fill('\0');
  std::memcpy(phraseBuffer.data(), kRewritten, sizeof kRewritten);
}

void serve(FakeStock* stock) { fake = stock; }

StockLibraryResult loadFake(const std::filesystem::path& path) {
  fake->libraryPath = path;
  return std::shared_ptr<StockLibrary>(std::make_shared<FakeLibrary>());
}

StockAudioConfig testConfig() {
  StockAudioConfig config;
  config.paths = cadence::stockPathsUnder("/stock", "libs/libvendor.so", "spotter_models/activation/main");
  config.preset = kTestVqePreset;
  config.capture = kTestCaptureLayout;
  return config;
}

void enableCommands(StockAudioConfig& config) {
  config.commandWords = true;
  config.paths.commandModel = kTestCommandModel;
}

StockAudioConfig commandConfig() {
  auto config = testConfig();
  enableCommands(config);
  return config;
}

std::vector<float> filled(int count, float value) { return std::vector<float>(static_cast<std::size_t>(count), value); }

StockFixture::StockFixture() {
  const auto& capture = kTestCaptureLayout;
  stock.mics = static_cast<int>(capture.microphones.size());
  stock.speakers = static_cast<int>(capture.speakers.size());
  serve(&stock);
  stock.outputs[kOmni] = filled(stock.outputFrames, 0.0f);
  stock.outputs[kOmniWithGain] = filled(stock.outputFrames, 0.0f);
  stock.outputs[kRawMainMic] = filled(stock.outputFrames, 0.0f);
  stock.outputs[kBnr] = filled(stock.outputFrames, 0.0f);
}

StockFixture::~StockFixture() {
  CHECK(stock.violations == std::vector<std::string>{});
  serve(nullptr);
}

}  // namespace cadence::fake_stock

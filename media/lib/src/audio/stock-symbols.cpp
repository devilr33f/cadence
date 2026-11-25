#include "audio/stock-symbols.h"

#include <cstddef>
#include <utility>

namespace cadence {

namespace {

class SymbolBinder {
 public:
  explicit SymbolBinder(const StockLibrary& library) : library_(library) {}

  template <typename Fn>
  void operator()(const char* name, Fn*& slot) {
    if (missing_)
      return;
    void* address = library_.symbol(name);
    if (address == nullptr)
      missing_ = StockError{StockErrorCode::MissingSymbol, name};
    else
      slot = reinterpret_cast<Fn*>(address);
  }

  [[nodiscard]] std::optional<StockError> missing() const { return missing_; }

 private:
  const StockLibrary& library_;
  std::optional<StockError> missing_;
};

void releaseVqeError(const StockSymbols& symbols, stock_abi::VqeError* error) { symbols.vqeErrorFree(error); }
void releaseMessage(const StockSymbols& symbols, char* message) { symbols.messageFree(message); }

}  // namespace

std::optional<StockError> bindVqeSymbols(const StockLibrary& library, StockSymbols& symbols) {
  SymbolBinder bind(library);
  bind("YandexVqeC_Config_CreateFromPreset", symbols.configCreate);
  bind("YandexVqeC_Config_Free", symbols.configFree);
  bind("YandexVqeC_Config_GetInputChunkSize", symbols.chunkFrames);
  bind("YandexVqeC_Config_GetProcessingFrame", symbols.outputFrames);
  bind("YandexVqeC_Config_GetInputSamplingRate", symbols.inputRate);
  bind("YandexVqeC_Config_GetOutputSamplingRate", symbols.outputRate);
  bind("YandexVqeC_Config_GetMicsCount", symbols.micCount);
  bind("YandexVqeC_Config_GetSpeakersCount", symbols.speakerCount);
  bind("YandexVqeC_Create", symbols.vqeCreate);
  bind("YandexVqeC_Free", symbols.vqeFree);
  bind("YandexVqeC_Process", symbols.vqeProcess);
  bind("YandexVqeC_GetOutputChannelData", symbols.vqeOutput);
  bind("YandexVqeC_Error_GetMessage", symbols.vqeErrorMessage);
  bind("YandexVqeC_Error_Free", symbols.vqeErrorFree);
  return bind.missing();
}

std::optional<StockError> bindSpotterSymbols(const StockLibrary& library, StockSymbols& symbols) {
  SymbolBinder bind(library);
  bind("spotter_conf_read", symbols.confRead);
  bind("spotter_conf_free", symbols.confFree);
  bind("spotter_conf_native_sample_rate", symbols.confNativeRate);
  bind("spotter_conf_get_required_channel_count", symbols.confChannelCount);
  bind("spotter_create_multichannel", symbols.spotterCreate);
  bind("spotter_destroy", symbols.spotterDestroy);
  bind("spotter_submit_raw_data", symbols.spotterSubmit);
  bind("spotter_get_next_event", symbols.spotterNextEvent);
  // spotter messages come from the library's malloc, so they go back to the free() it resolves
  bind("free", symbols.messageFree);
  return bind.missing();
}

std::optional<StockError> bindMultiSpotterSymbols(const StockLibrary& library, StockSymbols& symbols) {
  auto bound = symbols;
  SymbolBinder bind(library);
  bind("spotter_multi_conf_create", bound.multiConfCreate);
  bind("spotter_multi_conf_attach_spotter_conf", bound.multiConfAttach);
  bind("spotter_multi_conf_detach_component", bound.multiConfDetach);
  bind("spotter_multi_conf_free", bound.multiConfFree);
  bind("spotter_create_from_multi_conf", bound.spotterCreateFromMulti);
  bind("spotter_get_current_component_id", bound.currentComponent);
  bind("spotter_get_current_phrase", bound.currentPhrase);
  if (auto missing = bind.missing())
    return missing;
  symbols = bound;
  return std::nullopt;
}

std::optional<StockError> takeVqeError(const StockSymbols& symbols, stock_abi::VqeError* raw, StockErrorCode code) {
  const StockHandle<stock_abi::VqeError, releaseVqeError> error(raw, {&symbols});
  if (!error)
    return std::nullopt;
  return StockError{code, text::boundedMessage(symbols.vqeErrorMessage(error.get()))};
}

std::string takeSpotterMessage(const StockSymbols& symbols, char* raw, std::string_view fallback) {
  const StockHandle<char, releaseMessage> message(raw, {&symbols});
  return message ? text::boundedMessage(message.get()) : std::string(fallback);
}

std::optional<StockError> readSpotterConf(const StockSymbols& symbols, const std::filesystem::path& modelDir,
                                          SpotterConfHandle& conf) {
  char* message = nullptr;
  conf.reset(symbols.confRead(modelDir.c_str(), &message));
  auto detail = takeSpotterMessage(symbols, message, "no model config");
  if (!conf)
    return StockError{StockErrorCode::SpotterConfigFailed, std::move(detail)};
  return std::nullopt;
}

SpotterChannelCounts spotterChannelCounts(const StockSymbols& symbols, stock_abi::SpotterConf* conf) {
  SpotterChannelCounts counts{};
  for (std::size_t type = 0; type < counts.size(); ++type)
    counts[type] = symbols.confChannelCount(conf, static_cast<int>(type));
  return counts;
}

}  // namespace cadence

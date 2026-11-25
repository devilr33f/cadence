#pragma once

#include "audio/spotter-layout.h"
#include "audio/stock-error.h"
#include "audio/stock-library.h"
#include "audio/stock-types.h"

#include <filesystem>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence {

// stock entry points, bound in three groups: a spotter failure leaves the vqe engine usable, and a missing
// multi spotter call leaves alice listening alone
struct StockSymbols {
  stock_abi::VqeConfigCreateFromPreset* configCreate = nullptr;
  stock_abi::VqeConfigFree* configFree = nullptr;
  stock_abi::VqeConfigGetInt* chunkFrames = nullptr;
  stock_abi::VqeConfigGetInt* outputFrames = nullptr;
  stock_abi::VqeConfigGetInt* inputRate = nullptr;
  stock_abi::VqeConfigGetInt* outputRate = nullptr;
  stock_abi::VqeConfigGetInt* micCount = nullptr;
  stock_abi::VqeConfigGetInt* speakerCount = nullptr;
  stock_abi::VqeCreate* vqeCreate = nullptr;
  stock_abi::VqeFree* vqeFree = nullptr;
  stock_abi::VqeProcess* vqeProcess = nullptr;
  stock_abi::VqeGetOutput* vqeOutput = nullptr;
  stock_abi::VqeErrorMessage* vqeErrorMessage = nullptr;
  stock_abi::VqeErrorFree* vqeErrorFree = nullptr;
  stock_abi::SpotterConfRead* confRead = nullptr;
  stock_abi::SpotterConfFree* confFree = nullptr;
  stock_abi::SpotterConfNativeRate* confNativeRate = nullptr;
  stock_abi::SpotterConfChannelCount* confChannelCount = nullptr;
  stock_abi::SpotterCreateMultichannel* spotterCreate = nullptr;
  stock_abi::SpotterDestroy* spotterDestroy = nullptr;
  stock_abi::SpotterSubmit* spotterSubmit = nullptr;
  stock_abi::SpotterNextEvent* spotterNextEvent = nullptr;
  stock_abi::MessageFree* messageFree = nullptr;
  stock_abi::SpotterMultiConfCreate* multiConfCreate = nullptr;
  stock_abi::SpotterMultiConfAttach* multiConfAttach = nullptr;
  stock_abi::SpotterMultiConfDetach* multiConfDetach = nullptr;
  stock_abi::SpotterMultiConfFree* multiConfFree = nullptr;
  stock_abi::SpotterCreateFromMultiConf* spotterCreateFromMulti = nullptr;
  stock_abi::SpotterCurrentComponent* currentComponent = nullptr;
  stock_abi::SpotterCurrentPhrase* currentPhrase = nullptr;
};

// each binds until the first missing symbol, which it names
std::optional<StockError> bindVqeSymbols(const StockLibrary& library, StockSymbols& symbols);
std::optional<StockError> bindSpotterSymbols(const StockLibrary& library, StockSymbols& symbols);
// optional, so all or nothing: a missing symbol leaves every multi entry point unset
std::optional<StockError> bindMultiSpotterSymbols(const StockLibrary& library, StockSymbols& symbols);

template <typename T, auto Release>
struct StockDeleter {
  const StockSymbols* symbols = nullptr;

  void operator()(T* handle) const {
    if (handle != nullptr)
      Release(*symbols, handle);
  }
};

template <typename T, auto Release>
using StockHandle = std::unique_ptr<T, StockDeleter<T, Release>>;

inline void releaseConfig(const StockSymbols& symbols, stock_abi::VqeConfig* config) { symbols.configFree(config); }
inline void releaseVqe(const StockSymbols& symbols, stock_abi::Vqe* vqe) { symbols.vqeFree(vqe); }
inline void releaseSpotterConf(const StockSymbols& symbols, stock_abi::SpotterConf* conf) { symbols.confFree(conf); }
inline void releaseSpotter(const StockSymbols& symbols, stock_abi::Spotter* spotter) { symbols.spotterDestroy(spotter); }

using ConfigHandle = StockHandle<stock_abi::VqeConfig, releaseConfig>;
using VqeHandle = StockHandle<stock_abi::Vqe, releaseVqe>;
using SpotterConfHandle = StockHandle<stock_abi::SpotterConf, releaseSpotterConf>;
using SpotterHandle = StockHandle<stock_abi::Spotter, releaseSpotter>;

// takes ownership of a returned vqe error, copying its message before it is freed
std::optional<StockError> takeVqeError(const StockSymbols& symbols, stock_abi::VqeError* raw, StockErrorCode code);

// spotter messages may accompany success, so the caller decides whether one means failure
std::string takeSpotterMessage(const StockSymbols& symbols, char* raw, std::string_view fallback);

std::optional<StockError> readSpotterConf(const StockSymbols& symbols, const std::filesystem::path& modelDir,
                                          SpotterConfHandle& conf);
// how many channels of each spotter channel type the conf requires
SpotterChannelCounts spotterChannelCounts(const StockSymbols& symbols, stock_abi::SpotterConf* conf);

}  // namespace cadence

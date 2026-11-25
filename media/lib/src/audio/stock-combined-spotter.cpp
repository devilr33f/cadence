#include "audio/stock-combined-spotter.h"

#include <algorithm>
#include <cstddef>
#include <optional>
#include <string>
#include <string_view>
#include <utility>

namespace cadence {

namespace {

std::optional<StockError> checkCombinedRate(std::string_view model, int rate) {
  if (rate == kCombinedSpotterRate)
    return std::nullopt;
  return StockError{StockErrorCode::SpotterConfigFailed, std::string(model) + " model rate " + std::to_string(rate) +
                                                             ", the combined spotter needs " +
                                                             std::to_string(kCombinedSpotterRate)};
}

// the combined spotter feeds every component from one channel set: the most any of them asks for of each
// type. a negative count is kept so spotterLayout refuses it
SpotterChannelCounts unionCounts(const SpotterChannelCounts& first, const SpotterChannelCounts& second) {
  SpotterChannelCounts counts{};
  for (std::size_t type = 0; type < counts.size(); ++type) {
    const int least = std::min(first[type], second[type]);
    counts[type] = least < 0 ? least : std::max(first[type], second[type]);
  }
  return counts;
}

Result<CombinedSpotter, StockError> build(const StockSymbols& symbols, SpotterConfHandle alice,
                                          SpotterConfHandle commands, SpotterLayout layout,
                                          MultiConfHandle& multiConf, SpotterHandle& spotter) {
  if (auto error = multiConf.open())
    return *error;
  // alice first, as stock maind attaches them
  const auto aliceAttached = multiConf.attach(std::move(alice));
  if (!aliceAttached.ok())
    return aliceAttached.error();
  const auto commandsAttached = multiConf.attach(std::move(commands));
  if (!commandsAttached.ok())
    return commandsAttached.error();

  // one thread and events as they come, since cadenced drains them after every chunk
  const auto& types = layout.channelTypes;
  spotter.reset(symbols.spotterCreateFromMulti(multiConf.get(), kCombinedSpotterRate, static_cast<int>(types.size()),
                                               types.data(), nullptr, nullptr, false, 0));
  if (!spotter)
    return StockError{StockErrorCode::SpotterCreateFailed, "no combined spotter"};
  return CombinedSpotter{aliceAttached.value(), commandsAttached.value(), std::move(layout)};
}

}  // namespace

Result<CombinedSpotter, StockError> openCombinedSpotter(const StockLibrary& library, StockSymbols& symbols,
                                                        const StockPaths& paths, int outputRate,
                                                        MultiConfHandle& multiConf, SpotterHandle& spotter) {
  if (auto missing = bindMultiSpotterSymbols(library, symbols))
    return *missing;
  if (!paths.commandModel)
    return StockError{StockErrorCode::SpotterConfigFailed, "no command model"};
  if (outputRate != kCombinedSpotterRate)
    return StockError{StockErrorCode::ConfigMismatch, "output rate " + std::to_string(outputRate) +
                                                          ", the combined spotter needs " +
                                                          std::to_string(kCombinedSpotterRate)};

  SpotterConfHandle alice{nullptr, {&symbols}};
  if (auto error = readSpotterConf(symbols, paths.activationModel, alice))
    return *error;
  SpotterConfHandle commands{nullptr, {&symbols}};
  if (auto error = readSpotterConf(symbols, *paths.commandModel, commands)) {
    error->detail.insert(0, "command model: ");
    return *error;
  }
  if (auto error = checkCombinedRate("alice", symbols.confNativeRate(alice.get())))
    return *error;
  if (auto error = checkCombinedRate("command", symbols.confNativeRate(commands.get())))
    return *error;
  const auto layout = spotterLayout(
      unionCounts(spotterChannelCounts(symbols, alice.get()), spotterChannelCounts(symbols, commands.get())));
  if (!layout.ok())
    return layout.error();

  auto built = build(symbols, std::move(alice), std::move(commands), layout.value(), multiConf, spotter);
  if (!built.ok()) {
    spotter.reset();
    multiConf.reset();
  }
  return built;
}

}  // namespace cadence

#pragma once

#include <filesystem>
#include <functional>
#include <optional>
#include <span>
#include <string_view>

namespace cadence {

// where the stock library looks for its vqe models, beside the board's library under the stock root
inline constexpr std::string_view kVqeModelsDir = "vqe_models";

struct StockPaths {
  std::filesystem::path library;
  std::filesystem::path vqeModels;
  std::filesystem::path activationModel;
  // set only when command words are wanted, and never required
  std::optional<std::filesystem::path> commandModel;
};

using PathCheck = std::function<bool(const std::filesystem::path&)>;

// the first of the board's roots that is a directory. an override is used as given and never falls back to them
std::optional<std::filesystem::path> resolveStockRoot(std::span<const std::filesystem::path> roots,
                                                      const PathCheck& isDirectory,
                                                      const std::optional<std::filesystem::path>& rootOverride = std::nullopt);
// the library and the activation model as the board names them, relative to its stock root
StockPaths stockPathsUnder(const std::filesystem::path& root, const std::filesystem::path& library,
                           const std::filesystem::path& activationModel);
// the vqe models are not required: a board's firmware may ship none although its stock config names them
std::optional<std::filesystem::path> firstMissingStockPath(const StockPaths& paths, const PathCheck& exists);

bool directoryExists(const std::filesystem::path& path);
bool pathExists(const std::filesystem::path& path);

}  // namespace cadence

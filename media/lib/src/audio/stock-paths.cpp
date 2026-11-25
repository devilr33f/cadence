#include "audio/stock-paths.h"

#include <system_error>

namespace cadence {

std::optional<std::filesystem::path> resolveStockRoot(std::span<const std::filesystem::path> roots,
                                                      const PathCheck& isDirectory,
                                                      const std::optional<std::filesystem::path>& rootOverride) {
  if (rootOverride)
    return isDirectory(*rootOverride) ? rootOverride : std::nullopt;
  for (const auto& root : roots) {
    if (isDirectory(root))
      return root;
  }
  return std::nullopt;
}

StockPaths stockPathsUnder(const std::filesystem::path& root, const std::filesystem::path& library,
                           const std::filesystem::path& activationModel) {
  return {
      .library = root / library,
      .vqeModels = root / kVqeModelsDir,
      .activationModel = root / activationModel,
      .commandModel = std::nullopt,
  };
}

std::optional<std::filesystem::path> firstMissingStockPath(const StockPaths& paths, const PathCheck& exists) {
  for (const auto* path : {&paths.library, &paths.activationModel}) {
    if (!exists(*path))
      return *path;
  }
  return std::nullopt;
}

bool directoryExists(const std::filesystem::path& path) {
  std::error_code ignored;
  return std::filesystem::is_directory(path, ignored);
}

bool pathExists(const std::filesystem::path& path) {
  std::error_code ignored;
  return std::filesystem::exists(path, ignored);
}

}  // namespace cadence

#include "audio/stock-paths.h"

#include <catch2/catch_test_macros.hpp>

#include <filesystem>
#include <set>


TEST_CASE("derives stock paths from the board's names under the resolved root") {
  const auto paths = cadence::stockPathsUnder("/vendor/root", "lib/libvendor.so", "models/activation/main");

  CHECK(paths.library == std::filesystem::path("/vendor/root/lib/libvendor.so"));
  CHECK(paths.vqeModels == std::filesystem::path("/vendor/root/vqe_models"));
  CHECK(paths.activationModel == std::filesystem::path("/vendor/root/models/activation/main"));
  CHECK_FALSE(paths.commandModel.has_value());
}

TEST_CASE("names the first missing required stock path, never the vqe models or the command model") {
  auto paths = cadence::stockPathsUnder("/stock", "lib/libvendor.so", "models/activation/main");
  paths.commandModel = "/data/command";
  std::set<std::filesystem::path> present = {paths.library, paths.activationModel};
  const auto exists = [&present](const std::filesystem::path& path) { return present.count(path) != 0; };

  // a firmware may ship no vqe_models directory although its stock config names one
  CHECK_FALSE(cadence::firstMissingStockPath(paths, exists).has_value());
  present.erase(paths.activationModel);
  CHECK(cadence::firstMissingStockPath(paths, exists) == paths.activationModel);
  present.erase(paths.library);
  CHECK(cadence::firstMissingStockPath(paths, exists) == paths.library);
}

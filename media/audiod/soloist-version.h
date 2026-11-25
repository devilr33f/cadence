#pragma once

#include <cstdint>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

// what soloist --version says of its build (reference §6.2): `soloist <version> build <epoch> (<yyyymmdd>) …`
struct SoloistBuild {
  // 1-32 of [A-Za-z0-9._+-]
  std::string version;
  // unix seconds the build was made; spotify stops it 90 days later
  std::int64_t epoch = 0;
  // yyyymmdd
  std::string date;

  bool operator==(const SoloistBuild&) const = default;
};

// the first line that reads so
std::optional<SoloistBuild> parseSoloistVersion(std::string_view output);

}  // namespace cadence::audiod

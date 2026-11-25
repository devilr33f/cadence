#include "audiod/soloist-version.h"

#include <limits>
#include <vector>

namespace cadence::audiod {

namespace {

constexpr std::size_t kMaxVersion = 32;
constexpr std::size_t kDateDigits = 8;

bool digits(std::string_view text) {
  if (text.empty())
    return false;
  for (const char c : text)
    if (c < '0' || c > '9')
      return false;
  return true;
}

bool versionWord(std::string_view word) {
  if (word.empty() || word.size() > kMaxVersion)
    return false;
  for (const char c : word) {
    const bool alnum = (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z');
    if (!alnum && c != '.' && c != '-' && c != '_' && c != '+')
      return false;
  }
  return true;
}

bool space(char c) { return c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v'; }

std::vector<std::string_view> words(std::string_view line) {
  std::vector<std::string_view> found;
  std::size_t at = 0;
  while (at < line.size()) {
    while (at < line.size() && space(line[at]))
      ++at;
    const auto start = at;
    while (at < line.size() && !space(line[at]))
      ++at;
    if (at > start)
      found.push_back(line.substr(start, at - start));
  }
  return found;
}

std::optional<std::int64_t> epochOf(std::string_view word) {
  if (!digits(word))
    return std::nullopt;
  std::int64_t epoch = 0;
  constexpr auto kMax = std::numeric_limits<std::int64_t>::max();
  for (const char c : word) {
    const auto digit = static_cast<std::int64_t>(c - '0');
    if (epoch > (kMax - digit) / 10)
      return std::nullopt;
    epoch = epoch * 10 + digit;
  }
  return epoch;
}

std::optional<SoloistBuild> parseLine(std::string_view line) {
  const auto found = words(line);
  if (found.size() < 5 || found[0] != "soloist" || !versionWord(found[1]) || found[2] != "build")
    return std::nullopt;
  const auto epoch = epochOf(found[3]);
  auto date = found[4];
  if (!epoch || date.size() != kDateDigits + 2 || !date.starts_with('(') || !date.ends_with(')'))
    return std::nullopt;
  date = date.substr(1, kDateDigits);
  if (!digits(date))
    return std::nullopt;
  return SoloistBuild{.version = std::string(found[1]), .epoch = *epoch, .date = std::string(date)};
}

}  // namespace

std::optional<SoloistBuild> parseSoloistVersion(std::string_view output) {
  while (!output.empty()) {
    const auto end = output.find('\n');
    if (auto build = parseLine(output.substr(0, end)))
      return build;
    if (end == std::string_view::npos)
      break;
    output.remove_prefix(end + 1);
  }
  return std::nullopt;
}

}  // namespace cadence::audiod

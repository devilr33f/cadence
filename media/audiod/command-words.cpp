#include "audiod/command-words.h"

#include <algorithm>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::string_view kWhitespace = " \t\n\v\f\r";

std::string_view trimmed(std::string_view text) {
  const auto first = text.find_first_not_of(kWhitespace);
  if (first == std::string_view::npos)
    return {};
  return text.substr(first, text.find_last_not_of(kWhitespace) - first + 1);
}

}  // namespace

CommandWords::CommandWords(std::vector<std::string> words) : words_(std::move(words)) {}

std::optional<std::uint32_t> CommandWords::id(std::string_view phrase) const {
  const auto word = trimmed(phrase);
  if (word.empty())
    return std::nullopt;
  const auto found = std::find(words_.begin(), words_.end(), word);
  if (found == words_.end())
    return std::nullopt;
  return static_cast<std::uint32_t>(found - words_.begin());
}

}  // namespace cadence::audiod

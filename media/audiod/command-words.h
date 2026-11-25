#pragma once

#include <cstddef>
#include <cstdint>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::audiod {

// the command words cadenced acts on, from its Configure (its own policy vocabulary, never the model's). a phrase the
// command model reports leaves audiod as the index of the word it matches: exactly, once the whitespace around it is
// gone, as v1 matched its fixed words. the phrases are what the owner said: never logged
class CommandWords {
 public:
  CommandWords() = default;
  explicit CommandWords(std::vector<std::string> words);

  [[nodiscard]] std::optional<std::uint32_t> id(std::string_view phrase) const;
  [[nodiscard]] std::size_t size() const { return words_.size(); }

 private:
  std::vector<std::string> words_;
};

}  // namespace cadence::audiod

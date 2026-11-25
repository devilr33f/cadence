#include "audiod/command-words.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

using cadence::audiod::CommandWords;

TEST_CASE("a phrase is the index of the configured word it matches, whitespace around it aside") {
  const CommandWords words(std::vector<std::string>{"стоп", "хватит", "пауза", "тише", "громче"});
  CHECK(words.id("стоп") == std::optional<std::uint32_t>(0));
  CHECK(words.id("тише") == std::optional<std::uint32_t>(3));
  CHECK(words.id(" \t\n\v\f\rгромче\r\n") == std::optional<std::uint32_t>(4));
}

TEST_CASE("a phrase matches only a whole configured word, exactly") {
  const CommandWords words(std::vector<std::string>{"тише", "turn it down"});
  CHECK(words.id("turn it down") == std::optional<std::uint32_t>(1));
  CHECK_FALSE(words.id("тише тише").has_value());
  CHECK_FALSE(words.id("тиш").has_value());
  CHECK_FALSE(words.id("Turn it down").has_value());
  CHECK_FALSE(words.id("turn  it down").has_value());
  CHECK_FALSE(words.id("").has_value());
  CHECK_FALSE(words.id(" \n").has_value());
  CHECK_FALSE(CommandWords().id("тише").has_value());
}

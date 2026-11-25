#pragma once

#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::sim {

// one line of a sim script, parsed: the verb and its arguments. `send` takes the rest of its line verbatim, as one
// argument (its json can hold spaces)
struct Command {
  int line = 0;
  std::string verb;
  std::vector<std::string> args;

  [[nodiscard]] bool is(const char* v) const { return verb == v; }
  // the verb with its line, for error messages
  [[nodiscard]] std::string where() const { return verb + " (line " + std::to_string(line) + ")"; }
};

// the verbs a script may use and the arguments each takes: maxArgs < 0 means any number. send is alone: its json is
// the rest of the line, not tokens
struct VerbSpec {
  const char* verb;
  int minArgs;
  int maxArgs;
};

const VerbSpec* findVerb(const std::string& verb);

// splits one line into whitespace-separated tokens (quotes never: a path with spaces is not a script's argument)
std::vector<std::string> tokenize(const std::string& line);

// parses a whole script: blank lines and #-comments skipped. nullopt and the offending line in error
std::optional<std::vector<Command>> parseScript(const std::string& text, std::string* error);

}  // namespace cadence::screen::sim

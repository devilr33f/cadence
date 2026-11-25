// the script parser's unit test: a plain main (no catch2, so the sim's tests run wherever it builds)
#include "sim/script.h"

#include <cstdio>
#include <string>

using namespace cadence::screen::sim;

namespace {

int failures = 0;

void check(bool ok, const std::string& what) {
  if (ok) return;
  failures++;
  std::printf("fail: %s\n", what.c_str());
}

void parses() {
  std::string error;
  auto script = parseScript(
      "# a walkthrough\n"
      "start --size 1920x1080 --lang ru --headless\n"
      "\n"
      "   # indented comment\n"
      "press RIGHT\n"
      "hold BACK 800\n"
      "wait 500\n"
      "wait-for route:title 5000\n"
      "screenshot out/home.png\n"
      "route\n"
      "focus\n"
      "context\n"
      "send {\"toast\": {\"text\": \"привет\"}}\n"
      "send-file cards.binpb\n"
      "rpc-answer catalog.browse browse.json\n"
      "drop-link\n"
      "bands 0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 255\n"
      "quit\n",
      &error);
  check(script.has_value(), "parses: " + error);
  if (!script) return;
  check(script->size() == 15, "parses: 15 commands, got " + std::to_string(script->size()));
  const Command& start = (*script)[0];
  check(start.is("start") && start.line == 2, "parses: start on line 2");
  check(start.args.size() == 5, "parses: start flags");
  const Command& send = (*script)[9];
  check(send.args.size() == 1 && send.args[0] == "{\"toast\": {\"text\": \"привет\"}}", "parses: send keeps its json");
  check((*script)[13].is("bands") && (*script)[13].args.size() == 16, "parses: one 16-band frame");
  check((*script)[14].is("quit"), "parses: quit last");
}

void rejects() {
  struct Case {
    const char* text;
    const char* what;
  };
  const Case cases[] = {
      {"fly RIGHT", "unknown verb"},
      {"press", "press takes an argument"},
      {"hold BACK", "hold takes two"},
      {"wait 1 2", "wait takes one"},
      {"send", "send needs json"},
      {"screenshot a b", "screenshot takes one"},
      {"bands 1 2", "bands takes sixteen"},
  };
  for (const auto& c : cases) {
    std::string error;
    auto script = parseScript(c.text, &error);
    check(!script.has_value(), std::string("rejects: ") + c.what + " (" + c.text + ")");
    check(!error.empty(), "rejects: says why");
  }
}

void blankAndComments() {
  std::string error;
  auto script = parseScript("\n\n   \n# only comments\n", &error);
  check(script.has_value() && script->empty(), "blank: empty script is empty");
}

}  // namespace

int main() {
  parses();
  rejects();
  blankAndComments();
  if (failures == 0) {
    std::printf("sim-parser: ok\n");
    return 0;
  }
  std::printf("sim-parser: %d failure(s)\n", failures);
  return 1;
}

#include "sim/script.h"

#include <cctype>

namespace cadence::screen::sim {

namespace {

// the script's whole vocabulary; the runner maps each verb to what it does (sim/sim.cpp). send is handled before
// this table: its argument is the rest of the line, never tokenized
constexpr VerbSpec kVerbs[] = {
    {"start", 0, -1},      // start [--size WxH] [--lang ru|en] [--headless] [--vsync] [--async-images] [--hud]
    {"press", 1, 1},       // press KEY
    {"hold", 2, 2},        // hold KEY MS
    {"wait", 1, 1},        // wait MS
    {"wait-for", 1, 2},    // wait-for SELECTOR [TIMEOUT_MS]
    {"screenshot", 1, 1},  // screenshot PATH.png
    {"route", 0, 0},       // print the route
    {"focus", 0, 0},       // print the focused ref
    {"context", 0, 0},     // print the screen context
    {"send", -1, -1},      // send ENVELOPE-JSON (the rest of the line)
    {"send-file", 1, 1},   // send-file PATH (a .binpb frame, as the golden fixtures are)
    {"send-json", 1, 1},   // send-json PATH (a file holding what send takes: {"alarms_summary": {...}})
    {"bands", 16, 16},     // bands B0 ... B15: one audiod-style analysis frame, each 0-255
    {"pcm", 0, 1},         // pcm [MONO_NS|now]: one 512-frame stereo snapshot (0 = the output's timing unknown)
    {"pcm-file", 1, 1},    // pcm-file PATH: f32 stereo 48 khz streamed into the visualizer in real time, looping
    {"pcm-stop", 0, 0},    // pcm-stop: the pcm-file stream stops
    {"rpc-answer", 2, 2},  // rpc-answer METHOD JSON-FILE
    {"rpc-calls", 1, 1},   // rpc-calls METHOD: print how often the screen called it, and its last args
    {"drop-link", 0, 0},   // cadenced goes away, as a restart would
    {"perf", 0, 1},        // perf [reset|LABEL]: the frame profile since the last reset
    {"quit", 0, 0},
};

}  // namespace

const VerbSpec* findVerb(const std::string& verb) {
  for (const auto& v : kVerbs) {
    if (verb == v.verb) return &v;
  }
  return nullptr;
}

std::vector<std::string> tokenize(const std::string& line) {
  std::vector<std::string> out;
  std::size_t i = 0;
  while (i < line.size()) {
    while (i < line.size() && std::isspace(static_cast<unsigned char>(line[i])) != 0) i++;
    const std::size_t start = i;
    while (i < line.size() && std::isspace(static_cast<unsigned char>(line[i])) == 0) i++;
    if (i > start) out.emplace_back(line, start, i - start);
  }
  return out;
}

std::optional<std::vector<Command>> parseScript(const std::string& text, std::string* error) {
  std::vector<Command> out;
  std::size_t at = 0;
  int lineNo = 0;
  while (at <= text.size()) {
    const std::size_t nl = text.find('\n', at);
    const std::string line = text.substr(at, nl == std::string::npos ? std::string::npos : nl - at);
    at = nl == std::string::npos ? text.size() + 1 : nl + 1;
    lineNo++;
    const auto tokens = tokenize(line);
    if (tokens.empty() || tokens[0][0] == '#') continue;
    Command c;
    c.line = lineNo;
    c.verb = tokens[0];
    if (c.verb == "send") {
      // the json is the rest of the line after the verb
      const std::size_t rest = line.find_first_not_of(" \t", line.find(c.verb) + c.verb.size());
      if (rest == std::string::npos) {
        if (error != nullptr) *error = "line " + std::to_string(lineNo) + ": send needs a json object";
        return std::nullopt;
      }
      c.args.emplace_back(line, rest, std::string::npos);
      out.push_back(std::move(c));
      continue;
    }
    const VerbSpec* spec = findVerb(c.verb);
    if (spec == nullptr) {
      if (error != nullptr) *error = "line " + std::to_string(lineNo) + ": unknown verb " + c.verb;
      return std::nullopt;
    }
    c.args.assign(tokens.begin() + 1, tokens.end());
    const int n = static_cast<int>(c.args.size());
    if (n < spec->minArgs || (spec->maxArgs >= 0 && n > spec->maxArgs)) {
      if (error != nullptr) {
        *error = "line " + std::to_string(lineNo) + ": " + c.verb + " takes " + std::to_string(spec->minArgs) +
                 (spec->maxArgs < 0 ? " or more argument(s)" : (spec->minArgs == spec->maxArgs ? " argument(s)" : " to " + std::to_string(spec->maxArgs) + " argument(s)")) +
                 ", got " + std::to_string(n);
      }
      return std::nullopt;
    }
    out.push_back(std::move(c));
  }
  return out;
}

}  // namespace cadence::screen::sim

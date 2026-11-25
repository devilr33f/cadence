#include "app/config-json.h"

#include <cstdlib>
#include <string>

namespace cadence::screen::app {

namespace {

bool space(char c) { return c == ' ' || c == '\n' || c == '\t' || c == '\r'; }

// the offset of the value under a top-level key (past its colon and spaces), or npos
std::size_t valueAt(std::string_view json, std::string_view key) {
  int depth = 0;
  bool expectKey = false;
  for (std::size_t i = 0; i < json.size(); i++) {
    const char c = json[i];
    if (c == '"') {
      // a string: a key when at depth 1 right after { or ,
      std::size_t j = i + 1;
      std::string text;
      while (j < json.size() && json[j] != '"') {
        if (json[j] == '\\' && j + 1 < json.size()) j++;
        text.push_back(json[j]);
        j++;
      }
      if (depth == 1 && expectKey && text == key) {
        std::size_t k = j + 1;
        while (k < json.size() && space(json[k])) k++;
        if (k >= json.size() || json[k] != ':') return std::string_view::npos;
        k++;
        while (k < json.size() && space(json[k])) k++;
        return k < json.size() ? k : std::string_view::npos;
      }
      expectKey = false;
      i = j;
      continue;
    }
    if (c == '{' || c == '[') {
      depth++;
      expectKey = c == '{';
    } else if (c == '}' || c == ']') {
      depth--;
    } else if (c == ',') {
      expectKey = depth == 1;
    }
  }
  return std::string_view::npos;
}

}  // namespace

std::optional<double> topLevelNumber(std::string_view json, std::string_view key) {
  const auto k = valueAt(json, key);
  if (k == std::string_view::npos) return std::nullopt;
  const std::string rest(json.substr(k, 32));
  char* end = nullptr;
  const double v = std::strtod(rest.c_str(), &end);
  if (end == rest.c_str()) return std::nullopt;
  return v;
}

std::optional<std::string> topLevelString(std::string_view json, std::string_view key) {
  auto k = valueAt(json, key);
  if (k == std::string_view::npos || json[k] != '"') return std::nullopt;
  std::string out;
  for (k++; k < json.size(); k++) {
    char c = json[k];
    if (c == '"') return out;
    if (c == '\\') {
      if (++k >= json.size()) return std::nullopt;
      switch (json[k]) {
        case '"': c = '"'; break;
        case '\\': c = '\\'; break;
        case '/': c = '/'; break;
        // \u and control escapes: not in a url, so not taken
        default: return std::nullopt;
      }
    }
    out.push_back(c);
  }
  return std::nullopt;
}

}  // namespace cadence::screen::app

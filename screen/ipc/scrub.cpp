#include "ipc/scrub.h"

#include <cctype>

namespace cadence::screen::ipc {

namespace {

constexpr std::string_view kRedacted = "<redacted>";

bool isSpace(char c) { return c == ' ' || c == '\t' || c == '\n' || c == '\r'; }

bool isSchemeChar(char c) {
  return std::isalnum(static_cast<unsigned char>(c)) != 0 || c == '+' || c == '-' || c == '.';
}

// the scheme's start when `://` begins at i, else npos
std::size_t schemeStart(std::string_view s, std::size_t i) {
  if (s.substr(i, 3) != "://") return std::string_view::npos;
  std::size_t j = i;
  while (j > 0 && isSchemeChar(s[j - 1])) j--;
  if (j == i || std::isalpha(static_cast<unsigned char>(s[j])) == 0) return std::string_view::npos;
  return j;
}

std::size_t tokenEnd(std::string_view s, std::size_t i) {
  while (i < s.size() && !isSpace(s[i]) && s[i] != '"' && s[i] != '\'') i++;
  return i;
}

// a path may hold spaces (a title in a file name): it runs to the line's end, a quote, or the ": " of a message after it
std::size_t pathEnd(std::string_view s, std::size_t i) {
  while (i < s.size() && s[i] != '"' && s[i] != '\'' && s[i] != '\n' && s.substr(i, 2) != ": ") i++;
  while (i > 0 && i <= s.size() && (s[i - 1] == ':' || isSpace(s[i - 1]))) i--;
  return i;
}

}  // namespace

std::string scrubThirdParty(std::string_view in) {
  std::string out;
  out.reserve(in.size());
  std::size_t i = 0;
  while (i < in.size()) {
    const char c = in[i];
    if (c == '"' || c == '\'') {
      const std::size_t close = in.find(c, i + 1);
      out.append(kRedacted);
      i = close == std::string_view::npos ? in.size() : close + 1;
      continue;
    }
    if (c == ':') {
      if (const std::size_t start = schemeStart(in, i); start != std::string_view::npos) {
        out.erase(out.size() - (i - start));
        out.append(kRedacted);
        i = tokenEnd(in, i);
        continue;
      }
    }
    // an absolute path or one in a word ("file:/x" was handled above): a slash starting a token
    if (c == '/' && (i == 0 || isSpace(in[i - 1]) || in[i - 1] == '=' || in[i - 1] == '(' || in[i - 1] == '[')) {
      out.append(kRedacted);
      i = pathEnd(in, i);
      continue;
    }
    // control characters would break a line
    out.push_back(static_cast<unsigned char>(c) < 0x20 || c == 0x7f ? ' ' : c);
    i++;
  }
  // a trailing newline from mpv is not part of the line
  while (!out.empty() && out.back() == ' ') out.pop_back();
  return out;
}

bool isLogToken(std::string_view token) {
  if (token.empty() || token.size() > 64) return false;
  for (char c : token) {
    const bool ok = (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == ':' || c == '-';
    if (!ok) return false;
  }
  return true;
}

}  // namespace cadence::screen::ipc

#include "audiod/child-log.h"

#include "text.h"

#include <array>
#include <limits>
#include <string>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::string_view kEllipsis = "\xe2\x80\xa6";
constexpr std::string_view kReplacement = "\xef\xbf\xbd";
constexpr auto kWindow = std::chrono::seconds(1);

constexpr LogLevel kDebug = cadence_audiod_v1_LogLevel_LOG_LEVEL_DEBUG;
constexpr LogLevel kInfo = cadence_audiod_v1_LogLevel_LOG_LEVEL_INFO;
constexpr LogLevel kWarn = cadence_audiod_v1_LogLevel_LOG_LEVEL_WARN;
constexpr LogLevel kError = cadence_audiod_v1_LogLevel_LOG_LEVEL_ERROR;

constexpr std::array<std::pair<std::string_view, LogLevel>, 5> kLevelTokens{{
    {"DEBUG", kDebug},
    {"INFO", kInfo},
    {"WARNING", kWarn},
    {"WARN", kWarn},
    {"ERROR", kError},
}};

char upper(char c) { return c >= 'a' && c <= 'z' ? static_cast<char>(c - 'a' + 'A') : c; }

bool alphanumeric(char c) { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'); }

// valid utf-8 for the protobuf string, a nul (which would end nanopb's) replaced too, cut with … when it does not fit
// or the line was cut
std::string lineText(std::string_view raw, bool& cut) {
  std::string clean;
  clean.reserve(raw.size());
  for (const char c : raw) {
    if (c == '\0')
      clean.append(kReplacement);
    else
      clean.push_back(c);
  }
  auto text = text::boundedUtf8(clean, std::numeric_limits<std::size_t>::max());
  if (cut || text.size() > kChildLineBytes) {
    cut = true;
    text = text::boundedUtf8(text, kChildLineBytes - kEllipsis.size());
    text.append(kEllipsis);
  }
  return text;
}

}  // namespace

std::optional<LogLevel> leadingLevel(std::string_view line) {
  for (const auto& [token, level] : kLevelTokens) {
    if (line.size() < token.size())
      continue;
    bool same = true;
    for (std::size_t i = 0; i < token.size() && same; ++i)
      same = upper(line[i]) == token[i];
    if (same && (line.size() == token.size() || !alphanumeric(line[token.size()])))
      return level;
  }
  return std::nullopt;
}

ChildLog::ChildLog(std::string_view source, ChildLogTarget target) : source_(source), target_(std::move(target)) {}

void ChildLog::feed(Stream stream, std::span<const char> bytes, Clock::time_point now) {
  auto& line = of(stream);
  for (const char byte : bytes) {
    if (byte == '\n') {
      if (!line.skipping)
        send(stream, line, false, now);
      line.bytes.clear();
      line.since.reset();
      line.skipping = false;
      line.continued = false;
    } else if (line.skipping) {
      continue;
    } else if (line.bytes.size() < kChildLineBytes) {
      if (line.bytes.empty())
        line.since = now;
      line.bytes.push_back(byte);
    } else {
      send(stream, line, true, now);
      line.bytes.clear();
      line.since.reset();
      line.skipping = true;
    }
  }
}

void ChildLog::finish(Stream stream, Clock::time_point now) {
  auto& line = of(stream);
  if (!line.skipping)
    send(stream, line, true, now);
  line.bytes.clear();
  line.since.reset();
  line.skipping = false;
  line.continued = false;
}

void ChildLog::close(Clock::time_point now) {
  finish(Stream::Out, now);
  finish(Stream::Err, now);
  roll(now, true);
}

void ChildLog::tick(Clock::time_point now) {
  for (const auto stream : {Stream::Out, Stream::Err}) {
    auto& line = of(stream);
    if (line.since && now >= *line.since + kChildPartialWait) {
      // the rest, whenever it comes, starts a line of its own, which says it continues this one
      send(stream, line, true, now);
      line.bytes.clear();
      line.since.reset();
      line.continued = true;
    }
  }
  roll(now, false);
}

std::optional<ChildLog::Clock::time_point> ChildLog::deadline() const {
  std::optional<Clock::time_point> next;
  const auto consider = [&](std::optional<Clock::time_point> at) {
    if (at && (!next || *at < *next))
      next = at;
  };
  for (const auto* line : {&out_, &err_}) {
    if (line->since)
      consider(*line->since + kChildPartialWait);
  }
  if (window_ && dropped_ > 0)
    consider(*window_ + kWindow);
  return next;
}

void ChildLog::send(Stream stream, Pending& line, bool cut, Clock::time_point now) {
  auto raw = std::string_view(line.bytes);
  if (!cut && raw.ends_with('\r'))
    raw.remove_suffix(1);
  if (raw.empty() || !target_ || !target_.forward->on())
    return;
  roll(now, false);
  if (!window_) {
    window_ = now;
    sent_ = 0;
  }
  if (sent_ >= kChildLinesPerSecond) {
    ++dropped_;
    return;
  }
  ++sent_;
  const auto level = leadingLevel(raw).value_or(stream == Stream::Err ? kWarn : kInfo);
  auto text = lineText(raw, cut);
  target_.sink(ChildLine{
      .source = source_, .level = level, .text = std::move(text), .truncated = cut, .continued = line.continued});
}

void ChildLog::roll(Clock::time_point now, bool force) {
  if (!window_ || (!force && now < *window_ + kWindow))
    return;
  if (dropped_ > 0 && target_ && target_.forward->on())
    target_.sink(ChildLine{
        .source = source_, .level = kWarn, .text = "dropped " + std::to_string(dropped_) + " lines", .truncated = false});
  dropped_ = 0;
  window_.reset();
}

}  // namespace cadence::audiod

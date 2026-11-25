#include "ipc/log.h"

#include "ipc/scrub.h"

#include <cinttypes>
#include <cstdio>

namespace cadence::screen::ipc {

Token::Token(std::string_view value) : value_(isLogToken(value) ? std::string(value) : std::string("invalid")) {}

std::string formatLine(std::string_view event, std::initializer_list<Field> fields) {
  std::string out(event);
  for (const auto& f : fields) {
    out.push_back(' ');
    out.append(f.key);
    out.push_back('=');
    if (const auto* t = std::get_if<Token>(&f.value)) {
      out.append(t->value());
    } else if (const auto* i = std::get_if<std::int64_t>(&f.value)) {
      out.append(std::to_string(*i));
    } else if (const auto* d = std::get_if<double>(&f.value)) {
      char buf[32];
      std::snprintf(buf, sizeof buf, "%.3f", *d);
      out.append(buf);
    } else if (const auto* b = std::get_if<bool>(&f.value)) {
      out.append(*b ? "true" : "false");
    }
  }
  return out;
}

std::vector<LogRecord> splitRecord(LogRecord record, std::size_t limit) {
  std::vector<LogRecord> out;
  if (record.text.size() <= limit) {
    out.push_back(std::move(record));
    return out;
  }
  std::string_view rest = record.text;
  bool first = true;
  while (!rest.empty()) {
    std::size_t cut = std::min(limit, rest.size());
    // never inside a utf-8 sequence
    while (cut < rest.size() && cut > 0 && (static_cast<unsigned char>(rest[cut]) & 0xc0) == 0x80) cut--;
    if (cut == 0) cut = std::min(limit, rest.size());
    LogRecord piece{record.source, record.level, std::string(rest.substr(0, cut)), false, !first};
    rest.remove_prefix(cut);
    piece.truncated = !rest.empty();
    out.push_back(std::move(piece));
    first = false;
  }
  if (!out.empty() && record.truncated) out.back().truncated = true;
  if (!out.empty() && record.continued) out.front().continued = true;
  return out;
}

Log& Log::get() {
  static Log log;
  return log;
}

void Log::setSink(Sink sink) {
  std::lock_guard lock(mu_);
  sink_ = std::move(sink);
}

void Log::setMinLevel(Level level) {
  std::lock_guard lock(mu_);
  min_ = level;
}

void Log::event(Level level, std::string_view event, std::initializer_list<Field> fields) {
  emit(LogRecord{"cadence-screen", level, formatLine(event, fields), false, false});
}

void Log::thirdParty(std::string_view source, Level level, std::string_view line) {
  std::string scrubbed = scrubThirdParty(line);
  if (scrubbed.empty()) return;
  emit(LogRecord{std::string(source.substr(0, 127)), level, std::move(scrubbed), false, false});
}

void Log::emit(LogRecord record) {
  Sink sink;
  {
    std::lock_guard lock(mu_);
    if (record.level < min_) return;
    sink = sink_;
  }
  static constexpr const char* kNames[] = {"debug", "info", "warn", "error"};
  std::fprintf(stderr, "%s %s %s\n", record.source.c_str(), kNames[static_cast<int>(record.level)], record.text.c_str());
  if (sink) {
    for (auto& piece : splitRecord(std::move(record))) sink(piece);
  }
}

cadence_screen_v1_LogLevel toProto(Level level) {
  switch (level) {
    case Level::Debug:
      return cadence_screen_v1_LogLevel_LOG_LEVEL_DEBUG;
    case Level::Info:
      return cadence_screen_v1_LogLevel_LOG_LEVEL_INFO;
    case Level::Warn:
      return cadence_screen_v1_LogLevel_LOG_LEVEL_WARN;
    case Level::Error:
      return cadence_screen_v1_LogLevel_LOG_LEVEL_ERROR;
  }
  return cadence_screen_v1_LogLevel_LOG_LEVEL_INFO;
}

}  // namespace cadence::screen::ipc

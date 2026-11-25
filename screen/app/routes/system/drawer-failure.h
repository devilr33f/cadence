#pragma once

#include "app/routes/core/failure.h"

#include <algorithm>
#include <cstdint>
#include <string>
#include <string_view>

// the assistant drawer's failed view, kept pure so its codes and translation
// markers are pinned without a renderer. no upstream text is part of the input.
namespace cadence::screen::routes::assistantfailure {

struct View {
  std::string reason;
  std::string source;
  std::string code;
};

inline std::string id(std::string_view s) {
  if (s.empty() || s.size() > 256) return {};
  for (const char c : s) {
    const bool ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' ||
                    c == '-' || c == '/' || c == ':' || c == '+' || c == '@';
    if (!ok) return {};
  }
  return std::string(s);
}

inline View of(std::string_view error, const cadence_v2_TurnFailure* f) {
  View v;
  const std::string code = failure::token(f != nullptr ? std::string_view(f->code) : error);
  const std::string stage = f != nullptr ? failure::token(f->stage) : std::string();
  if (f == nullptr) {
    v.reason = "[[assistant.failed_why]]";
    return v;
  }
  failure::Call c;
  c.method = "assistant";
  c.status = failure::token(error);
  c.code = code;
  c.failure = code;
  c.provider = id(f->provider);
  c.httpStatus = f->http_status >= 100 && f->http_status <= 599 ? static_cast<int>(f->http_status) : 0;
  c.waitedMs = f->waited_ms > 0 ? f->waited_ms : -1;

  if (code == "timeout") {
    std::string key = "assistant.failed_timeout";
    if (stage == "llm" || stage == "stt" || stage == "tool" || stage == "tts") key += "_" + stage;
    const std::int64_t ms = f->limit_ms > 0 ? f->limit_ms : f->waited_ms;
    if (ms > 0) {
      const auto seconds = std::max<std::int64_t>(1, (ms + 500) / 1000);
      v.reason = "[[" + key + "|seconds=" + std::to_string(seconds) + "]]";
    } else {
      v.reason = "[[assistant.failed_timeout_unknown]]";
    }
  } else if (code == "rate_limited") {
    v.reason = stage == "llm" ? "[[assistant.failed_rate_limited]]" : "[[error.reason.rate_limited]]";
  } else if (code == "auth") {
    v.reason = stage == "llm" ? "[[assistant.failed_auth]]" : "[[error.reason.auth]]";
  } else if (c.httpStatus > 0) {
    v.reason = stage == "llm" ? "[[assistant.failed_http|status=" + std::to_string(c.httpStatus) + "]]"
                              : "[[error.reason.server|status=" + std::to_string(c.httpStatus) + "]]";
  } else if (code == "provider_error" || code == "llm_failed") {
    v.reason = "[[assistant.failed_provider]]";
  } else if (code == "not_configured") {
    v.reason = "[[assistant.failed_not_configured]]";
  } else if (code == "budget") {
    v.reason = "[[assistant.failed_budget]]";
  } else if (code == "canceled" || code == "cancelled") {
    v.reason = "[[assistant.failed_cancelled]]";
  } else if (stage == "stt") {
    v.reason = "[[assistant.failed_stt]]";
  } else if (stage == "tts") {
    v.reason = "[[assistant.failed_tts]]";
  } else if (stage == "tool") {
    v.reason = "[[assistant.failed_tool]]";
  } else {
    v.reason = "[[assistant.failed_why]]";
  }

  v.source = c.provider;
  if (const std::string model = id(f->model); !model.empty()) v.source += (v.source.empty() ? "" : " · ") + model;
  if (v.source.empty()) v.source = failure::token(f->tool);
  const std::string how = failure::reasonLine(c);
  v.code = (stage.empty() ? "assistant" : stage) + (how.empty() ? "" : " · " + how);
  return v;
}

}  // namespace cadence::screen::routes::assistantfailure

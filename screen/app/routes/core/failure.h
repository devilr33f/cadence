#pragma once

#include <nlohmann/json.hpp>

#include <algorithm>
#include <cstdint>
#include <cstdio>
#include <string>
#include <string_view>

// a failed call as the screen shows it: what failed and how, as the server and cadenced said it (codes, the source,
// the upstream's http status, how long the screen waited), never put into words of its own. pure (the unit tests pin
// it); every field is an id, a code or a number (rule 8: no upstream url, text or user content reaches the screen)
namespace cadence::screen::routes::failure {

struct Call {
  std::string method;
  // the rpc status (unavailable, not_found, refused, expired, invalid, ...)
  std::string status;
  // the server's refusal code and failure code (catalog.RefusalOf)
  std::string code;
  std::string failure;
  std::string provider;
  std::string source;
  int httpStatus = 0;
  // how long the screen waited for the answer, -1 when unknown
  std::int64_t waitedMs = -1;
};

// a code as it may be shown: [a-z0-9_.-], at most 48 characters, else nothing (an unexpected value never reaches the
// screen as it came)
inline std::string token(std::string_view s) {
  if (s.empty() || s.size() > 48) return {};
  for (const char c : s) {
    const bool ok = (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-';
    if (!ok) return {};
  }
  return std::string(s);
}

// the answer of a call that did not succeed: status and its json ({code, failure, provider, source, httpStatus}, the
// resolve stream's {status, refused: {...}} too)
inline Call of(std::string_view method, std::string_view status, const nlohmann::json& json, std::int64_t waitedMs) {
  Call c;
  c.method = token(method);
  c.status = token(status);
  c.waitedMs = waitedMs;
  const auto* j = &json;
  if (json.is_object() && json.contains("refused") && json["refused"].is_object()) {
    if (const auto st = json.value("status", std::string()); !st.empty()) c.status = token(st);
    j = &json["refused"];
  }
  if (!j->is_object()) return c;
  const auto str = [&](const char* k) {
    const auto it = j->find(k);
    return it != j->end() && it->is_string() ? token(it->get<std::string>()) : std::string();
  };
  c.code = str("code");
  c.failure = str("failure");
  c.provider = str("provider");
  c.source = str("source");
  if (const auto it = j->find("httpStatus"); it != j->end() && it->is_number_integer()) {
    const auto v = it->get<std::int64_t>();
    if (v >= 100 && v <= 599) c.httpStatus = static_cast<int>(v);
  }
  return c;
}

// which provider and source failed: "torrplayer · anime365", or the fallback (the route's own provider) when the
// answer named none
inline std::string sourceLine(const Call& c, std::string_view fallback = {}) {
  std::string s = !c.provider.empty() ? c.provider : std::string(fallback);
  if (!c.source.empty()) s += (s.empty() ? "" : " · ") + c.source;
  return s;
}

// how it failed, as said: the failure code (else the refusal code, else the status), the upstream's http status,
// and the time waited when it ran out: "timeout · 10.0 s", "failed · HTTP 502", "not_found"
inline std::string reasonLine(const Call& c) {
  std::string s = !c.failure.empty() ? c.failure : !c.code.empty() ? c.code : c.status;
  const auto add = [&](const std::string& part) {
    if (part.empty()) return;
    s += (s.empty() ? "" : " · ") + part;
  };
  if (c.httpStatus > 0) add("HTTP " + std::to_string(c.httpStatus));
  const bool timedOut = c.failure == "timeout" || c.code == "timeout" || c.status == "timeout";
  if (timedOut && c.waitedMs >= 0) {
    char buf[32];
    std::snprintf(buf, sizeof buf, "%.1f s", static_cast<double>(c.waitedMs) / 1000.0);
    add(buf);
  }
  return s;
}

// the call itself, for the secondary line: "catalog.item → unavailable/timeout"
inline std::string detailLine(const Call& c) {
  std::string s = c.method.empty() ? std::string("call") : c.method;
  std::string how = c.status;
  if (!c.code.empty() && c.code != c.status) how += (how.empty() ? "" : "/") + c.code;
  if (!c.failure.empty() && c.failure != c.code) how += (how.empty() ? "" : "/") + c.failure;
  if (!how.empty()) s += " → " + how;
  return s;
}

// the plain words for a failure: a reason the viewer reads and, when there is an obvious next step, a hint, as the
// translator's [[key|arg=value]] markers (ru and en in i18n/screen). empty when no rule knows the failure: the card
// then shows the code alone
struct Words {
  std::string reason;
  std::string hint;
  bool operator==(const Words&) const = default;
};

namespace detail {

// a rule of the table: which failure it matches, and its words (an empty hint key: no obvious next step)
enum class Hint { None, Running, Token, OtherSource, OtherRelease, Wait, Network };
struct Rule {
  bool (*match)(const Call&);
  const char* reason;
  Hint hint;
};

inline bool is(const Call& c, std::string_view v) { return c.failure == v || c.code == v || c.status == v; }

inline constexpr Rule kRules[] = {
    // the release itself (resolving, the torrent's state)
    {[](const Call& c) { return c.failure == "no_seeders"; }, "error.reason.no_seeders", Hint::OtherRelease},
    {[](const Call& c) { return c.failure == "removed"; }, "error.reason.removed", Hint::OtherRelease},
    {[](const Call& c) { return c.failure == "blocked"; }, "error.reason.blocked", Hint::OtherSource},
    // the screen's own link to cadenced
    {[](const Call& c) { return c.failure == "no_link"; }, "error.reason.no_link", Hint::Network},
    {[](const Call& c) { return is(c, "timeout"); }, "error.reason.timeout", Hint::Running},
    {[](const Call& c) { return c.httpStatus == 401 || c.httpStatus == 403 || is(c, "auth") || is(c, "sign_in"); },
     "error.reason.auth", Hint::Token},
    {[](const Call& c) { return c.httpStatus == 404 || is(c, "not_found"); }, "error.reason.not_found", Hint::OtherSource},
    {[](const Call& c) { return c.httpStatus == 429 || is(c, "rate_limited"); }, "error.reason.rate_limited", Hint::Wait},
    {[](const Call& c) { return c.httpStatus >= 500; }, "error.reason.server", Hint::None},
    // the player's own failure (its code is mpv's)
    {[](const Call& c) { return c.method == "player"; }, "error.reason.player", Hint::OtherRelease},
    {[](const Call& c) { return is(c, "unavailable") || is(c, "network") || is(c, "offline"); }, "error.reason.unavailable",
     Hint::Running},
};

}  // namespace detail

// the table's first rule that matches: timeout says the seconds waited, a 5xx its status, «запущен» the provider
// (the call's, else fallback: the route's own); a hint that needs a provider and has none is left out
inline Words wordsFor(const Call& c, std::string_view fallbackProvider = {}) {
  const std::string provider = !c.provider.empty() ? c.provider : token(fallbackProvider);
  for (const auto& r : detail::kRules) {
    if (!r.match(c)) continue;
    Words w;
    const std::string_view key = r.reason;
    if (key == "error.reason.timeout") {
      // whole seconds, one at least (a decimal would need the language's separator; the code line has the tenths)
      const auto seconds = std::max<std::int64_t>(1, (c.waitedMs + 500) / 1000);
      w.reason = c.waitedMs >= 0 ? "[[error.reason.timeout|seconds=" + std::to_string(seconds) + "]]"
                                 : "[[error.reason.timeout_unknown]]";
    } else if (key == "error.reason.server") {
      w.reason = "[[error.reason.server|status=" + std::to_string(c.httpStatus) + "]]";
    } else {
      w.reason = "[[" + std::string(key) + "]]";
    }
    switch (r.hint) {
      case detail::Hint::None:
        break;
      case detail::Hint::Running:
        if (!provider.empty()) w.hint = "[[error.hint.running|provider=" + provider + "]]";
        break;
      case detail::Hint::Token:
        w.hint = "[[error.hint.token]]";
        break;
      case detail::Hint::OtherSource:
        w.hint = "[[error.hint.other_source]]";
        break;
      case detail::Hint::OtherRelease:
        // releases and voiceovers are a torrent source's: a stream from elsewhere (youtube, music) has none to pick
        if (c.method != "player" || provider.starts_with("torr")) w.hint = "[[error.hint.other_release]]";
        break;
      case detail::Hint::Wait:
        w.hint = "[[error.hint.wait]]";
        break;
      case detail::Hint::Network:
        w.hint = "[[error.hint.network]]";
        break;
    }
    return w;
  }
  return {};
}

}  // namespace cadence::screen::routes::failure

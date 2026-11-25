#include "app/routes/playback/rpc.h"

#include "app/routes/core/failure-card.h"
#include "app/routes/core/failure.h"
#include "ui/core/rml.h"

#include <cstring>
#include <vector>

namespace cadence::screen::routes {

bool PlaybackRpc::call(const std::string& method, const std::string& json, Done done, std::int64_t nowMs,
                     std::int64_t timeoutMs) {
  const std::string id = "playback-" + std::to_string(++seq_);
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_rpc_request_tag;
  auto& r = e.body.rpc_request;
  std::strncpy(r.id, id.c_str(), sizeof r.id - 1);
  std::strncpy(r.method, method.c_str(), sizeof r.method - 1);
  std::vector<char> body(json.begin(), json.end());
  body.push_back('\0');
  r.json = body.data();
  if (!send_ || !send_(e)) return false;
  pending_[id] = Pending{std::move(done), nowMs + timeoutMs};
  return true;
}

bool PlaybackRpc::take(const cadence_screen_v1_Envelope& m) {
  if (m.which_body != cadence_screen_v1_Envelope_rpc_response_tag) return false;
  const auto& r = m.body.rpc_response;
  const auto it = pending_.find(r.id);
  if (it == pending_.end()) return false;
  auto done = std::move(it->second.done);
  pending_.erase(it);
  RpcAnswer a;
  a.status = r.status;
  a.json = r.json != nullptr ? r.json : "";
  a.response = &r;
  if (done) done(a);
  return true;
}

void PlaybackRpc::tick(std::int64_t nowMs) {
  // collected first: a callback may call again or cancel everything
  std::vector<Done> expired;
  for (auto it = pending_.begin(); it != pending_.end();) {
    if (nowMs < it->second.deadlineMs) {
      ++it;
      continue;
    }
    expired.push_back(std::move(it->second.done));
    it = pending_.erase(it);
  }
  RpcAnswer a;
  a.status = "timeout";
  for (auto& done : expired)
    if (done) done(a);
}

std::string failureCard(const char* method, const RpcAnswer& a, std::int64_t askedMs, std::int64_t nowMs) {
  const auto json = nlohmann::json::parse(a.json.empty() ? std::string("{}") : a.json, nullptr, false);
  const auto why = failure::of(method, a.status, json.is_discarded() ? nlohmann::json::object() : json, nowMs - askedMs);
  ui::rml::ErrorCard e;
  e.title = "[[common.load_failed]]";
  failure::describe(e, why);
  return ui::rml::errorCard(e);
}

}  // namespace cadence::screen::routes

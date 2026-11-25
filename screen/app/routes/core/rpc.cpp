#include "app/routes/core/rpc.h"

#include "ipc/codec.h"

#include <cstring>
#include <vector>

namespace cadence::screen::routes {

namespace {

Json parse(const char* text) {
  if (text == nullptr || *text == '\0') return Json::object();
  auto j = Json::parse(text, nullptr, false);
  return j.is_discarded() ? Json::object() : j;
}

}  // namespace

std::string RpcClient::call(const std::string& method, const Json& args, Handlers handlers, bool subscribe) {
  // ids are this client's, apart from the card host's (its own prefix)
  const std::string id = "b" + std::to_string(++next_);
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_rpc_request_tag);
  auto& r = out->body.rpc_request;
  ipc::copyField(r.id, id);
  ipc::copyField(r.method, method);
  r.json = out.keep(args.dump());
  r.subscribe = subscribe;
  if (!send_ || !send_(out.get())) {
    if (handlers.response) handlers.response("unavailable", Json{{"code", "unavailable"}});
    return {};
  }
  calls_[id] = Call{std::move(handlers), subscribe, false, 0, {}, nowMs_};
  return id;
}

void RpcClient::cancel(const std::string& id) {
  const auto it = calls_.find(id);
  if (it == calls_.end()) return;
  // the server ends a call's work on its cancel, answered or not (a media.play's resolve stops with it)
  const bool open = it->second.subscribe || !it->second.answered;
  calls_.erase(it);
  if (!open || !send_) return;
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_rpc_cancel_tag);
  ipc::copyField(out->body.rpc_cancel.id, id);
  send_(out.get());
}

void RpcClient::failAll() {
  auto calls = std::move(calls_);
  calls_.clear();
  for (auto& [id, c] : calls) {
    if (!c.answered) {
      if (c.handlers.response) c.handlers.response("unavailable", Json{{"code", "unavailable"}, {"failure", "no_link"}});
    } else if (c.handlers.event) {
      c.handlers.event(Json{{"status", "unavailable"}, {"refused", {{"code", "unavailable"}, {"failure", "no_link"}}}}, true);
    }
  }
}

void RpcClient::expire(std::int64_t nowMs) {
  nowMs_ = nowMs;
  std::vector<std::string> late;
  for (auto& [id, c] : calls_) {
    if (c.sentMs < 0) c.sentMs = nowMs;
    if (!c.answered && nowMs - c.sentMs >= kNoAnswerMs) late.push_back(id);
  }
  for (const auto& id : late) {
    const auto it = calls_.find(id);
    if (it == calls_.end()) continue;
    auto handler = it->second.handlers.response;
    const auto waited = nowMs - it->second.sentMs;
    // the server is told too, in case it still works on it
    cancel(id);
    if (handler) handler("unavailable", Json{{"code", "timeout"}, {"failure", "no_answer"}, {"waitedMs", waited}});
  }
}

bool RpcClient::response(const cadence_v2_RpcResponse& r) {
  const auto it = calls_.find(r.id);
  if (it == calls_.end()) return false;
  auto& c = it->second;
  c.answered = true;
  const std::string status = r.status;
  const auto json = parse(r.json);
  // a subscribed call stays open after an ok answer; any other ends it
  const bool ends = !c.subscribe || status != "ok";
  auto handler = c.handlers.response;
  if (ends) calls_.erase(it);
  if (handler) handler(status, json);
  if (!ends) deliver(r.id);
  return true;
}

bool RpcClient::event(const cadence_v2_RpcEvent& e) {
  const auto it = calls_.find(e.id);
  if (it == calls_.end()) return false;
  it->second.held[e.seq] = {parse(e.json), e.last};
  // progress is live while the method still runs; only sequence gaps wait
  deliver(e.id);
  return true;
}

void RpcClient::deliver(const std::string& id) {
  for (;;) {
    const auto it = calls_.find(id);
    if (it == calls_.end()) return;
    auto& c = it->second;
    const auto next = c.held.find(c.seq + 1);
    if (next == c.held.end()) return;
    auto [json, last] = std::move(next->second);
    c.held.erase(next);
    c.seq++;
    auto handler = c.handlers.event;
    if (last) calls_.erase(it);
    // the handler may cancel or start calls: nothing of the map is held across it
    if (handler) handler(json, last);
    if (last) return;
  }
}

}  // namespace cadence::screen::routes

#include "app/routes/system/page-rpc.h"

#include "app/routes/system/env.h"

#include <nlohmann/json.hpp>

#include <cstdio>
#include <cstring>

namespace cadence::screen::routes {

std::string jsonString(std::string_view s) {
  std::string out = "\"";
  for (const char sc : s) {
    const auto c = static_cast<unsigned char>(sc);
    if (c == '"') {
      out += "\\\"";
    } else if (c == '\\') {
      out += "\\\\";
    } else if (c < 0x20) {
      char buf[8];
      std::snprintf(buf, sizeof buf, "\\u%04x", c);
      out += buf;
    } else {
      out += static_cast<char>(c);
    }
  }
  return out + "\"";
}

std::string jsonField(std::string_view json, std::string_view key) {
  const auto j = nlohmann::json::parse(json.begin(), json.end(), nullptr, false);
  if (j.is_discarded() || !j.is_object()) return {};
  const auto it = j.find(std::string(key));
  if (it == j.end()) return {};
  if (it->is_string()) return it->get<std::string>();
  if (it->is_number_integer()) return std::to_string(it->get<long long>());
  if (it->is_number()) return it->dump();
  if (it->is_boolean()) return it->get<bool>() ? "true" : "false";
  return {};
}

std::string PageRpc::call(std::string_view method, std::string_view json, Answer answer, Event event, bool subscribe) {
  const std::string id = prefix_ + std::to_string(++seq_);
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_rpc_request_tag;
  auto& r = e.body.rpc_request;
  copyTo(r.id, id);
  copyTo(r.method, method);
  std::string body(json);
  r.json = body.data();
  r.subscribe = subscribe;
  if (!out_.send(e)) return {};
  calls_[id] = Open{std::move(answer), std::move(event), subscribe, false};
  return id;
}

void PageRpc::cancel(const std::string& id) {
  const auto it = calls_.find(id);
  if (it == calls_.end()) return;
  const bool subscribed = it->second.subscribed;
  calls_.erase(it);
  if (!subscribed) return;
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_rpc_cancel_tag;
  copyTo(e.body.rpc_cancel.id, id);
  out_.send(e);
}

bool PageRpc::message(const cadence_screen_v1_Envelope& m) {
  if (m.which_body == cadence_screen_v1_Envelope_rpc_response_tag) {
    const auto& r = m.body.rpc_response;
    const auto it = calls_.find(r.id);
    if (it == calls_.end()) return false;
    auto answer = it->second.answer;
    // a subscribed call stays open after an ok answer; anything else ends it
    if (!it->second.subscribed || std::strcmp(r.status, "ok") != 0) {
      calls_.erase(it);
    } else {
      it->second.answered = true;
    }
    if (answer) answer(r);
    return true;
  }
  if (m.which_body == cadence_screen_v1_Envelope_rpc_event_tag) {
    const auto& ev = m.body.rpc_event;
    const auto it = calls_.find(ev.id);
    if (it == calls_.end()) return false;
    auto event = it->second.event;
    if (ev.last) calls_.erase(it);
    if (event) event(ev);
    return true;
  }
  return false;
}

}  // namespace cadence::screen::routes

#pragma once

#include "app/ui-layer.h"

#include <functional>
#include <map>
#include <string>
#include <string_view>

namespace cadence::screen::routes {

// the routes' calls to the server over screen/v1's rpc passthrough (pkg/rpc's methods): an id of their own, the one
// answer and, for a subscribed call, its events until the last one or a cancel
class PageRpc {
 public:
  using Answer = std::function<void(const cadence_v2_RpcResponse&)>;
  using Event = std::function<void(const cadence_v2_RpcEvent&)>;

  explicit PageRpc(app::Outbox& out, std::string prefix = "i3-") : out_(out), prefix_(std::move(prefix)) {}

  // the call's id, or "" when there is no link
  std::string call(std::string_view method, std::string_view json, Answer answer = {}, Event event = {},
                   bool subscribe = false);
  void cancel(const std::string& id);
  // an rpc_response or rpc_event: true when it answered one of these calls
  bool message(const cadence_screen_v1_Envelope& m);
  [[nodiscard]] bool open(const std::string& id) const { return calls_.count(id) != 0; }

 private:
  struct Open {
    Answer answer;
    Event event;
    bool subscribed = false;
    bool answered = false;
  };
  app::Outbox& out_;
  std::string prefix_;
  std::uint64_t seq_ = 0;
  std::map<std::string, Open> calls_;
};

// a json result's top-level string or number field ("" or nullopt when absent); a tiny reader, never logged
std::string jsonField(std::string_view json, std::string_view key);

}  // namespace cadence::screen::routes

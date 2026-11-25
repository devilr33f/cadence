#pragma once

#include "cadence/screen/v1/screen.pb.h"

#include <nlohmann/json.hpp>

#include <cstdint>
#include <functional>
#include <map>
#include <string>

namespace cadence::screen::routes {

using Json = nlohmann::json;

// the screen's calls to the server's methods (pkg/rpc's names) over screen/v1's RpcRequest passthrough: cadenced
// relays them on the device link and answers unavailable itself without one. json in, json out (D5). a subscribed
// call's RpcEvent frames come in seq order (a gap is held until it fills). nothing about a call's content is logged
// (refs, queries and titles are private, rule 8). ui thread only
class RpcClient {
 public:
  using Send = std::function<bool(const cadence_screen_v1_Envelope&)>;
  struct Handlers {
    // the one answer: status is pkg/rpc's token (ok, invalid, refused, expired, unavailable, ...)
    std::function<void(const std::string& status, const Json& json)> response;
    // a subscribed call's next message; last ends it
    std::function<void(const Json& json, bool last)> event;
  };

  explicit RpcClient(Send send) : send_(std::move(send)) {}

  // the call's id, "" when the link took nothing (its response handler is told unavailable at once)
  std::string call(const std::string& method, const Json& args, Handlers handlers, bool subscribe = false);
  // drops the call's handlers; one still open on the server (unanswered, or subscribed) is cancelled there
  void cancel(const std::string& id);
  // the link went down: every open call ends as unavailable
  void failAll();
  // the clock's tick: a call the server never answered (its answer lost with a server restart) ends after
  // kNoAnswerMs as a timeout, so nothing waits on it forever; a subscribed call that was answered is the server's
  // to end
  void expire(std::int64_t nowMs);
  // past the server's longest wait before it answers: a play's screen bound (video's screenPlayTimeout, 75 s) and the
  // play tools' budget (90 s)
  static constexpr std::int64_t kNoAnswerMs = 120000;

  // true when the frame was one of this client's calls
  bool response(const cadence_v2_RpcResponse& r);
  bool event(const cadence_v2_RpcEvent& e);

  [[nodiscard]] std::size_t open() const { return calls_.size(); }

 private:
  struct Call {
    Handlers handlers;
    bool subscribe = false;
    bool answered = false;
    std::uint32_t seq = 0;
    std::map<std::uint32_t, std::pair<Json, bool>> held;
    // -1 until the first tick after it was sent
    std::int64_t sentMs = -1;
  };
  void deliver(const std::string& id);

  Send send_;
  std::map<std::string, Call> calls_;
  std::uint64_t next_ = 0;
  // the last tick's time: a call is stamped with it
  std::int64_t nowMs_ = -1;
};

}  // namespace cadence::screen::routes

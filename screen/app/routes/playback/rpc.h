#pragma once

#include "cadence/screen/v1/screen.pb.h"

#include <cstdint>
#include <functional>
#include <map>
#include <string>

namespace cadence::screen::routes {

// an rpc answer as the routes take it: status (pkg/rpc's token), the json and, for a method that answers cards, the
// page (the envelope's, valid only during the callback)
struct RpcAnswer {
  std::string status;
  std::string json;
  const cadence_v2_RpcResponse* response = nullptr;
  // no answer within the call's timeout: status "timeout"
  [[nodiscard]] bool ok() const { return status == "ok"; }
};

// a sheet's failed call as a card (failure.h): the source, the codes, the call, no action (BACK closes the sheet).
// askedMs is when the call went out (the time waited is shown on a timeout)
std::string failureCard(const char* method, const RpcAnswer& a, std::int64_t askedMs, std::int64_t nowMs);

// the routes' calls to server methods over the link (cadence.v2.RpcRequest through cadenced): ids of their own
// (playback-<n>), answers by id, a timeout per call. args and answers are never logged (refs, titles: rule 8)
class PlaybackRpc {
 public:
  using Send = std::function<bool(const cadence_screen_v1_Envelope&)>;
  using Done = std::function<void(const RpcAnswer&)>;

  explicit PlaybackRpc(Send send) : send_(std::move(send)) {}

  // false when the link is down (done is not called)
  bool call(const std::string& method, const std::string& json, Done done, std::int64_t nowMs,
            std::int64_t timeoutMs = 8000);
  // an rpc_response (or rpc_event) of the link: true when it was one of ours
  bool take(const cadence_screen_v1_Envelope& envelope);
  // times calls out
  void tick(std::int64_t nowMs);
  // forget every pending call without answering (a route closing)
  void cancelAll() { pending_.clear(); }
  [[nodiscard]] std::size_t pending() const { return pending_.size(); }

 private:
  struct Pending {
    Done done;
    std::int64_t deadlineMs;
  };
  Send send_;
  std::map<std::string, Pending> pending_;
  std::uint64_t seq_ = 0;
};

}  // namespace cadence::screen::routes

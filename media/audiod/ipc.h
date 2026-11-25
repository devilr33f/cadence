#pragma once

#include "audiod/framing.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/sys.h"

#include <array>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <unordered_map>
#include <vector>

namespace cadence::audiod {

// audiod's socket: unix SOCK_SEQPACKET, mode 0600, one client (cadenced) at a time. each received frame goes
// to the handler registered for its body, on the loop thread. an oversize or undecodable frame drops the
// client and the server listens on; a body without a handler is logged and skipped. every call belongs to the
// loop thread: a part whose work runs on another thread hands its result back through Loop::post first.
//
// a frame may carry fds (SCM_RIGHTS, received close-on-exec): only a body registered with onFd takes them, exactly one.
// a frame with fds for any other body, or with more than one, or whose control data came truncated, has every fd it
// brought closed and is skipped (ipc_fd_refused); so is an onFd body that came without its fd. the client stays
class IpcServer {
 public:
  using Handler = std::function<void(const Envelope& envelope)>;
  // the handler owns the fd from then on
  using FdHandler = std::function<void(const Envelope& envelope, UniqueFd fd)>;

  // replaces a stale socket at path, never another kind of file; nullptr, logged, when it cannot listen
  static std::unique_ptr<IpcServer> listen(Loop& loop, const Log& log, std::string path);
  // drops the client, without the client-gone hook, and removes the socket path
  ~IpcServer();

  IpcServer(const IpcServer&) = delete;
  IpcServer& operator=(const IpcServer&) = delete;

  // tag is the body's field number (cadence_audiod_v1_Envelope_<body>_tag); a later handler replaces one.
  // register before the loop runs: a handler must not register others
  void on(pb_size_t tag, Handler handler);
  // a body that comes with exactly one fd (BluetoothStream); register before the loop runs
  void onFd(pb_size_t tag, FdHandler handler);
  // runs whenever a connected client is dropped, whatever the reason; register before the loop runs
  void onClientGone(Loop::Callback hook);

  // false when no client is connected or the frame could not go out. a client that stops reading loses
  // frames once its socket is full (logged once per stall) and stays connected. any other send error shuts
  // the connection down, so the receive path drops the client: send itself never runs the client-gone hook,
  // and a part may send from inside its own state changes
  bool send(const Envelope& envelope);

  // closes the connection, as a protocol error does, and runs the client-gone hook before it returns; the
  // caller logs why
  void dropClient();
  [[nodiscard]] bool hasClient() const { return client_.valid(); }

 private:
  IpcServer(Loop& loop, const Log& log, std::string path, UniqueFd listener);

  void acceptClients();
  void receiveFrames();
  // the frame's body to its handler, with the fds it brought; any fd not handed over is closed
  void dispatch(const Envelope& envelope, std::vector<UniqueFd> fds, bool truncated);

  Loop& loop_;
  const Log& log_;
  std::string path_;
  UniqueFd listener_;
  std::optional<Loop::WatchId> listenerWatch_;
  UniqueFd client_;
  std::optional<Loop::WatchId> clientWatch_;
  bool stalled_ = false;
  // a send failed and the connection is shut down; the receive path drops it soon
  bool broken_ = false;
  std::unordered_map<pb_size_t, Handler> handlers_;
  std::unordered_map<pb_size_t, FdHandler> fdHandlers_;
  Loop::Callback clientGone_;
  std::array<std::uint8_t, kReceiveBufferBytes> received_{};
};

}  // namespace cadence::audiod

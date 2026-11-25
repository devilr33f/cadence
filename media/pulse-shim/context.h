#pragma once

#include "pulse-shim/abi.h"
#include "pulse-shim/introspect.h"
#include "pulse-shim/mainloop.h"

#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>

namespace cadence::pulse {

class Link;
class Stream;

// where the soloist pcm server listens when pa_context_connect names no unix: server
inline constexpr const char* kDefaultSocket = "/run/cadence/pcm.sock";

// a connection to the pcm server: one socket, and the link its playback streams share. a pa_context points at it
class Context : public std::enable_shared_from_this<Context> {
 public:
  explicit Context(std::shared_ptr<Loop> loop);
  ~Context();

  Context(const Context&) = delete;
  Context& operator=(const Context&) = delete;

  // another reference on the context behind a c pointer the caller still holds
  static std::shared_ptr<Context> retain(const pa_context* c);
  [[nodiscard]] pa_context* raw() const;
  [[nodiscard]] const std::shared_ptr<Loop>& loop() const { return loop_; }

  pa_context_state_t state();
  int error();
  void setError(int error);
  void setStateCallback(pa_context_notify_cb_t cb, void* userdata);

  // the state callback runs on this thread, which holds the mainloop lock (connect reaches READY before it returns
  // that way); another thread posts the change to the loop instead
  void setState(pa_context_state_t state);
  // same lock rule; the stream it was playing fails first
  void fail(int error);

  // runs work on the loop thread as the server's answer; a context that left READY meanwhile cancels it. null, with
  // BADSTATE, when it is not READY
  pa_operation* operation(std::function<void(Context&)> work);
  void succeed(pa_context_success_cb_t cb, void* userdata);

  // the client's sink input, under its own mutex
  template <typename F>
  auto withSinkInput(F f) {
    std::scoped_lock lock(sinkMutex_);
    return f(sinkInput_);
  }

  // the link its streams share, started with the first one; null unless READY
  std::shared_ptr<Link> link();
  // the sample spec of the stream being played, if any
  std::optional<pa_sample_spec> playing();

  // synchronous: CONNECTING, then READY or FAILED with CONNECTIONREFUSED
  int connect(const std::string& path);
  void disconnect();

  // the c caller's reference
  std::shared_ptr<Context> caller;

 private:
  // shutting the socket down reaches every handle the link took, so the server sees the end at once and the link's
  // threads return; the stream the link served, if still referenced, is handed back to be ended
  std::shared_ptr<Stream> close();

  const std::shared_ptr<Loop> loop_;

  std::mutex mutex_;
  pa_context_state_t state_ = PA_CONTEXT_UNCONNECTED;
  int error_ = PA_OK;
  pa_context_notify_cb_t onState_ = nullptr;
  void* stateData_ = nullptr;
  int socket_ = -1;

  std::mutex sinkMutex_;
  SinkInput sinkInput_;

  std::mutex linkMutex_;
  std::shared_ptr<Link> link_;
};

}  // namespace cadence::pulse

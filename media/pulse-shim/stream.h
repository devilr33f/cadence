#pragma once

#include "pulse-shim/abi.h"
#include "pulse-shim/buffer.h"
#include "pulse-shim/context.h"
#include "pulse-shim/link.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <span>
#include <utility>

namespace cadence::pulse {

// a playback stream; its bytes wait in the context link's buffer. a pa_stream points at it. stream.cpp holds the
// entry points that create, connect and end it, stream-write.cpp the data path, stream-timing.cpp the timing info
class Stream : public std::enable_shared_from_this<Stream> {
 public:
  enum class Callback { State, Started, Underflow };

  Stream(std::shared_ptr<Context> context, pa_sample_spec spec, std::size_t frame);
  ~Stream();

  Stream(const Stream&) = delete;
  Stream& operator=(const Stream&) = delete;

  // another reference on the stream behind a c pointer the caller still holds
  static std::shared_ptr<Stream> retain(const pa_stream* s);
  [[nodiscard]] pa_stream* raw() const;
  [[nodiscard]] Context& context() const { return *context_; }

  pa_stream_state_t state();
  std::uint32_t index();
  bool corked();
  void setCallback(Callback which, pa_stream_notify_cb_t cb, void* userdata);

  // dev, volume and the sync stream are ignored: the server plays at unity gain
  int connect(const pa_buffer_attr* attr, pa_stream_flags_t flags);
  int disconnect();
  // a connected stream ends; the bytes it has not sent are dropped
  void end(pa_stream_state_t state);
  void fail(int error);
  void event(StreamEvent event);

  // 0, or the error a refused write sets
  int write(std::span<const std::uint8_t> bytes, std::int64_t offset, pa_seek_mode_t seek);
  // the room below tlength; SIZE_MAX, with BADSTATE, for a stream that is not ready
  std::size_t writable();
  // cork and flush act at once, in order with the writes; their operations complete on the loop
  pa_operation* cork(bool on, pa_stream_success_cb_t cb, void* userdata);
  pa_operation* flush(pa_stream_success_cb_t cb, void* userdata);

  pa_operation* updateTiming(pa_stream_success_cb_t cb, void* userdata);
  // null until the first update completes, unless the stream updates it automatically
  const pa_timing_info* timingInfo();

  // the c caller's reference
  std::shared_ptr<Stream> caller;

 private:
  struct Notify {
    pa_stream_notify_cb_t cb = nullptr;
    void* userdata = nullptr;
  };

  [[nodiscard]] bool connected();
  // like the context's: the caller holds the mainloop lock, the i/o threads post the change instead
  void setState(pa_stream_state_t state);
  [[nodiscard]] std::size_t tlength(const pa_buffer_attr* attr) const;
  [[nodiscard]] std::size_t maxlength(const pa_buffer_attr* attr, std::size_t tlength) const;
  // the link and index of a ready stream
  std::optional<std::pair<std::shared_ptr<Link>, std::uint32_t>> served();
  // the buffer of a ready stream the link still serves: with changes it, peek reads it
  bool withBuffer(const std::function<void(Buffer&)>& f);
  bool peekBuffer(const std::function<void(const Buffer&)>& f);
  // completes on the loop: work runs, then cb hears whether the stream was still ready
  pa_operation* operation(pa_stream_success_cb_t cb, void* userdata, std::function<void(Stream&)> work);
  pa_operation* control(const std::function<void(Buffer&)>& f, std::optional<bool> corked, pa_stream_success_cb_t cb,
                        void* userdata);
  void snapshot();

  const std::shared_ptr<Context> context_;
  const pa_sample_spec spec_;
  const std::size_t frame_;

  std::mutex mutex_;
  // all of these under mutex_
  pa_stream_state_t state_ = PA_STREAM_UNCONNECTED;
  std::uint32_t index_ = PA_INVALID_INDEX;
  bool corked_ = false;
  bool autoTiming_ = false;
  std::shared_ptr<Link> link_;
  Notify onState_;
  Notify onStarted_;
  Notify onUnderflow_;
  // a snapshot was taken, so pa_stream_get_timing_info has something to point at
  bool timed_ = false;

  // what pa_stream_get_timing_info points at; read and written only under the mainloop lock
  pa_timing_info timing_{};
};

}  // namespace cadence::pulse

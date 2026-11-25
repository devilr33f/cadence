// the data path: writes into the buffer, its room, and the cork and flush that act in order with them
#include "pulse-shim/guard.h"
#include "pulse-shim/stream.h"

namespace cadence::pulse {

int Stream::write(std::span<const std::uint8_t> bytes, std::int64_t offset, pa_seek_mode_t seek) {
  if (state() != PA_STREAM_READY)
    return PA_ERR_BADSTATE;
  if (offset != 0 || seek != PA_SEEK_RELATIVE)
    return PA_ERR_NOTSUPPORTED;
  if (bytes.size() % frame_ != 0)
    return PA_ERR_INVALID;
  std::optional<std::uint64_t> writeIndex;
  const bool served = withBuffer([&](Buffer& buffer) {
    if (buffer.write(bytes))
      writeIndex = buffer.indexes().write;
  });
  if (!served)
    return PA_ERR_BADSTATE;
  if (!writeIndex)
    return PA_ERR_INVALID;
  // documented: write_index, and only it, follows every write
  timing_.write_index = static_cast<std::int64_t>(*writeIndex);
  return PA_OK;
}

std::size_t Stream::writable() {
  std::size_t room = 0;
  if (!peekBuffer([&](const Buffer& buffer) { room = buffer.room(); })) {
    context_->setError(PA_ERR_BADSTATE);
    return SIZE_MAX;
  }
  return room;
}

pa_operation* Stream::control(const std::function<void(Buffer&)>& f, std::optional<bool> corked,
                              pa_stream_success_cb_t cb, void* userdata) {
  if (!withBuffer(f)) {
    context_->setError(PA_ERR_BADSTATE);
    return nullptr;
  }
  if (corked) {
    std::scoped_lock lock(mutex_);
    corked_ = *corked;
  }
  // documented: a local pause or flush leaves the read index out of date until the next update
  timing_.read_index_corrupt = 1;
  return operation(cb, userdata, [](Stream&) {});
}

pa_operation* Stream::cork(bool on, pa_stream_success_cb_t cb, void* userdata) {
  return control([on](Buffer& buffer) { buffer.cork(on); }, on, cb, userdata);
}

pa_operation* Stream::flush(pa_stream_success_cb_t cb, void* userdata) {
  return control([](Buffer& buffer) { buffer.flush(); }, std::nullopt, cb, userdata);
}

}  // namespace cadence::pulse

using namespace cadence::pulse;

extern "C" {

int pa_stream_is_corked(const pa_stream* s) {
  return guarded(-1, [&] {
    const auto stream = Stream::retain(s);
    return stream ? (stream->corked() ? 1 : 0) : -1;
  });
}

std::size_t pa_stream_writable_size(const pa_stream* s) {
  return guarded(std::size_t{SIZE_MAX}, [&] {
    const auto stream = Stream::retain(s);
    return stream ? stream->writable() : SIZE_MAX;
  });
}

// the bytes are copied, then free_cb runs unless data is null; only appending (offset 0, PA_SEEK_RELATIVE) is supported
int pa_stream_write(pa_stream* p, const void* data, std::size_t nbytes, pa_free_cb_t free_cb, std::int64_t offset,
                    pa_seek_mode_t seek) {
  return guarded(-1, [&] {
    const auto stream = Stream::retain(p);
    if (!stream)
      return -1;
    if (data == nullptr && nbytes != 0) {
      stream->context().setError(PA_ERR_INVALID);
      return -1;
    }
    const std::span<const std::uint8_t> bytes(static_cast<const std::uint8_t*>(data), nbytes);
    if (const int error = stream->write(bytes, offset, seek); error != PA_OK) {
      stream->context().setError(error);
      return -1;
    }
    if (free_cb != nullptr && data != nullptr)
      free_cb(const_cast<void*>(data));
    return 0;
  });
}

pa_operation* pa_stream_cork(pa_stream* s, int b, pa_stream_success_cb_t cb, void* userdata) {
  return guarded(static_cast<pa_operation*>(nullptr), [&]() -> pa_operation* {
    const auto stream = Stream::retain(s);
    return stream ? stream->cork(b != 0, cb, userdata) : nullptr;
  });
}

pa_operation* pa_stream_flush(pa_stream* s, pa_stream_success_cb_t cb, void* userdata) {
  return guarded(static_cast<pa_operation*>(nullptr), [&]() -> pa_operation* {
    const auto stream = Stream::retain(s);
    return stream ? stream->flush(cb, userdata) : nullptr;
  });
}

}  // extern "C"

// creating, connecting and ending streams, their state and callbacks
#include "pulse-shim/stream.h"

#include "pulse-shim/guard.h"
#include "pulse-shim/operation.h"
#include "pulse-shim/sample.h"

#include <algorithm>
#include <atomic>

namespace cadence::pulse {

namespace {

// a stream that asks for no tlength gets 250 ms; one may ask for at most 2 s (the docs let the server choose)
constexpr pa_usec_t kDefaultTlengthUsec = 250000;
constexpr pa_usec_t kMaxTlengthUsec = 2000000;
// what a stream that asks for no maxlength may hold past tlength, at least
constexpr std::size_t kDefaultHeadroom = 65536;

std::atomic<std::uint32_t> nextIndex{0};

}  // namespace

Stream::Stream(std::shared_ptr<Context> context, pa_sample_spec spec, std::size_t frame)
    : context_(std::move(context)), spec_(spec), frame_(frame) {}

Stream::~Stream() {
  if (link_)
    link_->detach(index_);
}

std::shared_ptr<Stream> Stream::retain(const pa_stream* s) {
  if (s == nullptr)
    return nullptr;
  return const_cast<Stream*>(reinterpret_cast<const Stream*>(s))->weak_from_this().lock();
}

pa_stream* Stream::raw() const { return reinterpret_cast<pa_stream*>(const_cast<Stream*>(this)); }

pa_stream_state_t Stream::state() {
  std::scoped_lock lock(mutex_);
  return state_;
}

std::uint32_t Stream::index() {
  std::scoped_lock lock(mutex_);
  return index_;
}

bool Stream::corked() {
  std::scoped_lock lock(mutex_);
  return corked_;
}

void Stream::setCallback(Callback which, pa_stream_notify_cb_t cb, void* userdata) {
  std::scoped_lock lock(mutex_);
  auto& slot = which == Callback::State ? onState_ : which == Callback::Started ? onStarted_ : onUnderflow_;
  slot = {cb, userdata};
}

bool Stream::connected() {
  const auto current = state();
  return current == PA_STREAM_CREATING || current == PA_STREAM_READY;
}

void Stream::setState(pa_stream_state_t state) {
  Notify notify;
  {
    std::scoped_lock lock(mutex_);
    state_ = state;
    notify = onState_;
  }
  if (notify.cb != nullptr)
    notify.cb(raw(), notify.userdata);
  context_->loop()->signal();
}

void Stream::end(pa_stream_state_t state) {
  if (!connected())
    return;
  std::shared_ptr<Link> link;
  std::uint32_t index = 0;
  {
    std::scoped_lock lock(mutex_);
    link = std::move(link_);
    link_.reset();
    index = index_;
  }
  if (link)
    link->detach(index);
  setState(state);
}

void Stream::fail(int error) {
  if (!connected())
    return;
  context_->setError(error);
  end(PA_STREAM_FAILED);
}

void Stream::event(StreamEvent event) {
  Notify notify;
  {
    std::scoped_lock lock(mutex_);
    if (state_ != PA_STREAM_READY)
      return;
    notify = event == StreamEvent::Started ? onStarted_ : onUnderflow_;
  }
  if (notify.cb != nullptr)
    notify.cb(raw(), notify.userdata);
}

int Stream::connect(const pa_buffer_attr* attr, pa_stream_flags_t flags) {
  if (state() != PA_STREAM_UNCONNECTED || context_->state() != PA_CONTEXT_READY) {
    context_->setError(PA_ERR_BADSTATE);
    return -1;
  }
  const auto format = wireFormat(spec_);
  if (!format) {
    context_->setError(PA_ERR_NOTSUPPORTED);
    setState(PA_STREAM_FAILED);
    return -1;
  }
  const auto link = context_->link();
  if (!link) {
    context_->setError(PA_ERR_INTERNAL);
    setState(PA_STREAM_FAILED);
    return -1;
  }
  const bool startCorked = (flags & PA_STREAM_START_CORKED) != 0;
  const auto index = nextIndex.fetch_add(1, std::memory_order_relaxed);
  {
    std::scoped_lock lock(mutex_);
    index_ = index;
    corked_ = startCorked;
    autoTiming_ = (flags & PA_STREAM_AUTO_TIMING_UPDATE) != 0;
    link_ = link;
  }
  const auto target = tlength(attr);
  const auto replaced = link->attach(
      Served{index, weak_from_this(), spec_, Buffer(*format, target, maxlength(attr, target), startCorked), std::nullopt});
  setState(PA_STREAM_CREATING);
  const auto& loop = context_->loop();
  // one live stream per context: the newest wins, the one it replaced is killed
  if (replaced)
    loop->post([replaced = *replaced] {
      if (const auto killed = replaced.lock())
        killed->fail(PA_ERR_KILLED);
    });
  loop->post([weak = weak_from_this()] {
    if (const auto stream = weak.lock(); stream && stream->state() == PA_STREAM_CREATING)
      stream->setState(PA_STREAM_READY);
  });
  return 0;
}

int Stream::disconnect() {
  if (!connected()) {
    context_->setError(PA_ERR_BADSTATE);
    return -1;
  }
  end(PA_STREAM_TERMINATED);
  return 0;
}

std::size_t Stream::tlength(const pa_buffer_attr* attr) const {
  if (attr != nullptr && attr->tlength != UINT32_MAX) {
    const auto most = usecToBytes(kMaxTlengthUsec, spec_);
    return std::clamp(attr->tlength / frame_ * frame_, frame_, most);
  }
  return usecToBytes(kDefaultTlengthUsec, spec_);
}

// documented: a client may write past writable_size up to maxlength, so a write is refused only past it
std::size_t Stream::maxlength(const pa_buffer_attr* attr, std::size_t tlength) const {
  std::size_t most = std::max(4 * tlength, tlength + kDefaultHeadroom);
  if (attr != nullptr && attr->maxlength != UINT32_MAX && attr->maxlength >= tlength)
    most = attr->maxlength;
  return most / frame_ * frame_;
}

std::optional<std::pair<std::shared_ptr<Link>, std::uint32_t>> Stream::served() {
  std::scoped_lock lock(mutex_);
  if (state_ != PA_STREAM_READY || !link_)
    return std::nullopt;
  return std::pair{link_, index_};
}

bool Stream::withBuffer(const std::function<void(Buffer&)>& f) {
  const auto link = served();
  return link && link->first->with(link->second, f);
}

bool Stream::peekBuffer(const std::function<void(const Buffer&)>& f) {
  const auto link = served();
  return link && link->first->peek(link->second, f);
}

pa_operation* Stream::operation(pa_stream_success_cb_t cb, void* userdata, std::function<void(Stream&)> work) {
  return context_->operation([stream = shared_from_this(), cb, userdata, work = std::move(work)](Context&) {
    const bool ready = stream->state() == PA_STREAM_READY;
    if (ready)
      work(*stream);
    if (cb != nullptr)
      cb(stream->raw(), ready ? 1 : 0, userdata);
  });
}

}  // namespace cadence::pulse

using namespace cadence::pulse;

namespace {

pa_stream* newStream(pa_context* c, const pa_sample_spec* ss, const pa_channel_map* map) {
  const auto context = Context::retain(c);
  if (!context)
    return nullptr;
  const auto frame = ss == nullptr ? std::nullopt : frameSize(*ss);
  if (!frame || (map != nullptr && map->channels != ss->channels)) {
    context->setError(PA_ERR_INVALID);
    return nullptr;
  }
  auto stream = std::make_shared<Stream>(context, *ss, *frame);
  stream->caller = stream;
  return stream->raw();
}

void setNotify(pa_stream* s, Stream::Callback which, pa_stream_notify_cb_t cb, void* userdata) {
  if (const auto stream = Stream::retain(s))
    stream->setCallback(which, cb, userdata);
}

}  // namespace

extern "C" {

pa_stream* pa_stream_new(pa_context* c, const char*, const pa_sample_spec* ss, const pa_channel_map* map) {
  return guarded(static_cast<pa_stream*>(nullptr), [&] { return newStream(c, ss, map); });
}

// the property list is not kept
pa_stream* pa_stream_new_with_proplist(pa_context* c, const char*, const pa_sample_spec* ss, const pa_channel_map* map,
                                       pa_proplist*) {
  return guarded(static_cast<pa_stream*>(nullptr), [&] { return newStream(c, ss, map); });
}

void pa_stream_unref(pa_stream* s) {
  guarded([&] {
    if (s == nullptr)
      return;
    // the last reference may go here: nothing of the stream is touched afterwards
    const auto caller = std::move(reinterpret_cast<Stream*>(s)->caller);
  });
}

int pa_stream_connect_playback(pa_stream* s, const char*, const pa_buffer_attr* attr, pa_stream_flags_t flags,
                               const pa_cvolume*, pa_stream*) {
  return guarded(-1, [&] {
    const auto stream = Stream::retain(s);
    return stream ? stream->connect(attr, flags) : -1;
  });
}

int pa_stream_disconnect(pa_stream* s) {
  return guarded(-1, [&] {
    const auto stream = Stream::retain(s);
    return stream ? stream->disconnect() : -1;
  });
}

pa_stream_state_t pa_stream_get_state(const pa_stream* s) {
  return guarded(pa_stream_state_t{PA_STREAM_FAILED}, [&] {
    const auto stream = Stream::retain(s);
    return stream ? stream->state() : PA_STREAM_FAILED;
  });
}

std::uint32_t pa_stream_get_index(const pa_stream* s) {
  return guarded(std::uint32_t{PA_INVALID_INDEX}, [&] {
    const auto stream = Stream::retain(s);
    return stream ? stream->index() : std::uint32_t{PA_INVALID_INDEX};
  });
}

void pa_stream_set_state_callback(pa_stream* s, pa_stream_notify_cb_t cb, void* userdata) {
  guarded([&] { setNotify(s, Stream::Callback::State, cb, userdata); });
}

void pa_stream_set_started_callback(pa_stream* s, pa_stream_notify_cb_t cb, void* userdata) {
  guarded([&] { setNotify(s, Stream::Callback::Started, cb, userdata); });
}

void pa_stream_set_underflow_callback(pa_stream* s, pa_stream_notify_cb_t cb, void* userdata) {
  guarded([&] { setNotify(s, Stream::Callback::Underflow, cb, userdata); });
}

}  // extern "C"

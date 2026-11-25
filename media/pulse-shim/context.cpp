#include "pulse-shim/context.h"

#include "pulse-shim/guard.h"
#include "pulse-shim/link.h"
#include "pulse-shim/operation.h"
#include "pulse-shim/stream.h"

#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include <cstring>
#include <string_view>
#include <utility>

namespace cadence::pulse {

Context::Context(std::shared_ptr<Loop> loop) : loop_(std::move(loop)) {}

// a stream holds its context, so none is left to end here
Context::~Context() { close(); }

std::shared_ptr<Context> Context::retain(const pa_context* c) {
  if (c == nullptr)
    return nullptr;
  return const_cast<Context*>(reinterpret_cast<const Context*>(c))->weak_from_this().lock();
}

pa_context* Context::raw() const { return reinterpret_cast<pa_context*>(const_cast<Context*>(this)); }

pa_context_state_t Context::state() {
  std::scoped_lock lock(mutex_);
  return state_;
}

int Context::error() {
  std::scoped_lock lock(mutex_);
  return error_;
}

void Context::setError(int error) {
  std::scoped_lock lock(mutex_);
  error_ = error;
}

void Context::setStateCallback(pa_context_notify_cb_t cb, void* userdata) {
  std::scoped_lock lock(mutex_);
  onState_ = cb;
  stateData_ = userdata;
}

void Context::setState(pa_context_state_t state) {
  pa_context_notify_cb_t cb = nullptr;
  void* userdata = nullptr;
  {
    std::scoped_lock lock(mutex_);
    state_ = state;
    cb = onState_;
    userdata = stateData_;
  }
  if (cb != nullptr)
    cb(raw(), userdata);
  loop_->signal();
}

void Context::fail(int error) {
  setError(error);
  if (const auto stream = close())
    stream->end(PA_STREAM_FAILED);
  setState(PA_CONTEXT_FAILED);
}

pa_operation* Context::operation(std::function<void(Context&)> work) {
  if (state() != PA_CONTEXT_READY) {
    setError(PA_ERR_BADSTATE);
    return nullptr;
  }
  auto [op, raw] = Operation::start();
  loop_->post([context = shared_from_this(), op = std::move(op), work = std::move(work)] {
    if (context->state() == PA_CONTEXT_READY) {
      work(*context);
      op->finish(PA_OPERATION_DONE);
    } else {
      op->finish(PA_OPERATION_CANCELLED);
    }
    context->loop_->signal();
  });
  return raw;
}

void Context::succeed(pa_context_success_cb_t cb, void* userdata) {
  if (cb != nullptr)
    cb(raw(), 1, userdata);
}

std::shared_ptr<Link> Context::link() {
  int socket = -1;
  {
    std::scoped_lock lock(mutex_);
    if (state_ != PA_CONTEXT_READY)
      return nullptr;
    socket = socket_;
  }
  std::scoped_lock lock(linkMutex_);
  if (!link_)
    link_ = Link::start(shared_from_this(), socket);
  return link_;
}

std::optional<pa_sample_spec> Context::playing() {
  std::shared_ptr<Link> link;
  {
    std::scoped_lock lock(linkMutex_);
    link = link_;
  }
  return link ? link->spec() : std::nullopt;
}

std::shared_ptr<Stream> Context::close() {
  std::shared_ptr<Link> link;
  {
    std::scoped_lock lock(linkMutex_);
    link = std::move(link_);
    link_.reset();
  }
  const auto served = link ? link->stop().lock() : nullptr;
  int socket = -1;
  {
    std::scoped_lock lock(mutex_);
    socket = std::exchange(socket_, -1);
  }
  if (socket >= 0) {
    ::shutdown(socket, SHUT_RDWR);
    ::close(socket);
  }
  return served;
}

int Context::connect(const std::string& path) {
  if (state() != PA_CONTEXT_UNCONNECTED) {
    setError(PA_ERR_BADSTATE);
    return -1;
  }
  setState(PA_CONTEXT_CONNECTING);
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  const int socket = path.size() < sizeof address.sun_path ? ::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0) : -1;
  if (socket >= 0) {
    std::memcpy(address.sun_path, path.c_str(), path.size() + 1);
    if (::connect(socket, reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0) {
      {
        std::scoped_lock lock(mutex_);
        socket_ = socket;
      }
      setState(PA_CONTEXT_READY);
      return 0;
    }
    ::close(socket);
  }
  fail(PA_ERR_CONNECTIONREFUSED);
  return -1;
}

void Context::disconnect() {
  const auto current = state();
  if (current < PA_CONTEXT_CONNECTING || current > PA_CONTEXT_READY)
    return;
  if (const auto stream = close())
    stream->end(PA_STREAM_TERMINATED);
  setState(PA_CONTEXT_TERMINATED);
}

}  // namespace cadence::pulse

using namespace cadence::pulse;

namespace {

// the server pa_context_connect names, else PULSE_SERVER's, else the default: as pulse's own documentation resolves
// it, a unix: path alone (an abstract @name never reaches this shim's caller: soloist names none, and an unjailed
// soloist's launcher points PULSE_SERVER at the scratch's socket)
std::string socketPath(const char* server) {
  constexpr std::string_view kUnix = "unix:";
  const auto unixPath = [&](const char* named) {
    const std::string_view value(named);
    if (value.starts_with(kUnix) && value.size() > kUnix.size())
      return std::string(value.substr(kUnix.size()));
    return std::string();
  };
  if (server != nullptr) {
    if (const auto path = unixPath(server); !path.empty())
      return path;
  } else if (const char* env = ::getenv("PULSE_SERVER"); env != nullptr) {
    if (const auto path = unixPath(env); !path.empty())
      return path;
  }
  return kDefaultSocket;
}

}  // namespace

extern "C" {

// name is ignored
pa_context* pa_context_new(pa_mainloop_api* mainloop, const char*) {
  return guarded(static_cast<pa_context*>(nullptr), [&]() -> pa_context* {
    auto loop = Loop::fromApi(mainloop);
    if (!loop)
      return nullptr;
    auto context = std::make_shared<Context>(std::move(loop));
    context->caller = context;
    return context->raw();
  });
}

void pa_context_unref(pa_context* c) {
  guarded([&] {
    if (c == nullptr)
      return;
    // the last reference may go here: nothing of the context is touched afterwards
    const auto caller = std::move(reinterpret_cast<Context*>(c)->caller);
  });
}

// the flags and the spawn api are ignored: there is no daemon to spawn
int pa_context_connect(pa_context* c, const char* server, pa_context_flags_t, const pa_spawn_api*) {
  return guarded(-1, [&] {
    const auto context = Context::retain(c);
    return context ? context->connect(socketPath(server)) : -1;
  });
}

void pa_context_disconnect(pa_context* c) {
  guarded([&] {
    if (const auto context = Context::retain(c))
      context->disconnect();
  });
}

int pa_context_errno(const pa_context* c) {
  return guarded(int{PA_ERR_INVALID}, [&] {
    const auto context = Context::retain(c);
    return context ? context->error() : int{PA_ERR_INVALID};
  });
}

pa_context_state_t pa_context_get_state(const pa_context* c) {
  return guarded(pa_context_state_t{PA_CONTEXT_FAILED}, [&] {
    const auto context = Context::retain(c);
    return context ? context->state() : pa_context_state_t{PA_CONTEXT_FAILED};
  });
}

void pa_context_set_state_callback(pa_context* c, pa_context_notify_cb_t cb, void* userdata) {
  guarded([&] {
    if (const auto context = Context::retain(c))
      context->setStateCallback(cb, userdata);
  });
}

}  // extern "C"

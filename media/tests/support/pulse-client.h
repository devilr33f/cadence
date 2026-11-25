#pragma once

#include "audiod/sys.h"
#include "pcmframe/codec.h"
#include "pulse-shim/abi.h"
#include "support/eventually.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <pthread.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <mutex>
#include <span>
#include <string>
#include <type_traits>
#include <utility>
#include <variant>
#include <vector>

// a fake soloist pcm server on a temp socket and a threaded-mainloop client of the shim, driving it through its c api
// alone, as soloist does
namespace cadence::test_support::pulse {

using Bytes = std::vector<std::uint8_t>;

// 250 ms of float stereo at 44.1 kHz, the tlength a stream gets when it asks for none
inline constexpr std::size_t kDefaultTlength = 88200;
inline constexpr const char* kLoopThread = "pulse-mainloop";

inline std::string threadName() {
  std::array<char, 16> name{};
  ::pthread_getname_np(::pthread_self(), name.data(), name.size());
  return name.data();
}

// distinct nonzero bytes, so a lost, repeated or zeroed range shows
inline Bytes pattern(std::size_t from, std::size_t n) {
  Bytes bytes(n);
  for (std::size_t i = 0; i < n; ++i)
    bytes[i] = static_cast<std::uint8_t>((from + i) % 251 + 1);
  return bytes;
}

inline pcmframe::Format floatStereo() { return {pcmframe::Encoding::F32Le, 2, 44100}; }

inline pa_buffer_attr attr(std::uint32_t tlength, std::uint32_t maxlength = UINT32_MAX) {
  return {maxlength, tlength, UINT32_MAX, UINT32_MAX, UINT32_MAX};
}

// a message as the server decoded it, its pcm copied out of the decoder
struct Message {
  pcmframe::ToServer message;
  Bytes pcm;
};

// the server's end of the socket, decoding what the shim sends
class Server {
 public:
  explicit Server(audiod::UniqueFd socket) : socket_(std::move(socket)) {
    const timeval patience{5, 0};
    ::setsockopt(socket_.get(), SOL_SOCKET, SO_RCVTIMEO, &patience, sizeof patience);
  }

  // the next frame; a timeout or a closed socket fails the test
  pcmframe::ToServer next() {
    for (;;) {
      const auto decoded = decoder_.nextToServer();
      REQUIRE(decoded.ok());
      if (decoded.value()) {
        const auto& message = *decoded.value();
        if (const auto* pcm = std::get_if<pcmframe::Pcm>(&message)) {
          REQUIRE(pcm->bytes.size() <= pcmframe::kMaxPayload);
          held_.assign(pcm->bytes.begin(), pcm->bytes.end());
          return pcmframe::Pcm{held_};
        }
        return message;
      }
      std::array<std::uint8_t, 16 * 1024> chunk{};
      const auto n = ::recv(socket_.get(), chunk.data(), chunk.size(), 0);
      REQUIRE(n > 0);
      decoder_.push(std::span(chunk).first(static_cast<std::size_t>(n)));
    }
  }

  // pcm frames until n bytes arrived; any other message first fails the test
  Bytes pcm(std::size_t n) {
    Bytes bytes;
    while (bytes.size() < n) {
      const auto message = next();
      const auto* pcm = std::get_if<pcmframe::Pcm>(&message);
      REQUIRE(pcm != nullptr);
      bytes.insert(bytes.end(), pcm->bytes.begin(), pcm->bytes.end());
    }
    REQUIRE(bytes.size() == n);
    return bytes;
  }

  void played(std::uint64_t total) {
    Bytes wire;
    pcmframe::encode(pcmframe::Played{total}, wire);
    send(wire);
  }

  void send(const Bytes& wire) {
    REQUIRE(::send(socket_.get(), wire.data(), wire.size(), MSG_NOSIGNAL) == static_cast<ssize_t>(wire.size()));
  }

  // whether anything is waiting to be read, without waiting for it
  [[nodiscard]] bool idle() const {
    std::uint8_t byte = 0;
    return ::recv(socket_.get(), &byte, 1, MSG_DONTWAIT | MSG_PEEK) < 0 && errno == EAGAIN;
  }

  // 0 once the shim closed its end
  [[nodiscard]] ssize_t readSome() const {
    std::array<std::uint8_t, 64> chunk{};
    return ::recv(socket_.get(), chunk.data(), chunk.size(), 0);
  }

  void close() { socket_.reset(); }

 private:
  audiod::UniqueFd socket_;
  pcmframe::Decoder decoder_;
  Bytes held_;
};

// a listening unix socket at a temp path
class Listener {
 public:
  Listener() : path_(dir_.file("pcm.sock")) {
    sockaddr_un address{};
    address.sun_family = AF_UNIX;
    std::memcpy(address.sun_path, path_.c_str(), path_.size() + 1);
    fd_ = audiod::UniqueFd(::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0));
    REQUIRE(::bind(fd_.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
    REQUIRE(::listen(fd_.get(), 4) == 0);
  }

  Server accept() const {
    audiod::UniqueFd socket(::accept4(fd_.get(), nullptr, nullptr, SOCK_CLOEXEC));
    REQUIRE(socket.valid());
    return Server(std::move(socket));
  }

  [[nodiscard]] const std::string& path() const { return path_; }
  [[nodiscard]] std::string server() const { return "unix:" + path_; }

 private:
  TempDir dir_;
  std::string path_;
  audiod::UniqueFd fd_;
};

// what the callbacks saw, and on which thread
struct Seen {
  std::mutex mutex;
  std::vector<std::pair<pa_stream_state_t, std::string>> states;
  std::vector<std::string> started;
  std::vector<std::string> underflows;
  std::vector<std::pair<int, std::string>> successes;
  // index, sample spec and volume
  struct Info {
    std::uint32_t index;
    pa_sample_spec spec;
    std::vector<pa_volume_t> volume;
  };
  std::vector<Info> infos;

  void* ptr() { return this; }

  template <typename T>
  T copy(const T& field) {
    std::scoped_lock lock(mutex);
    return field;
  }

  std::vector<pa_stream_state_t> stateList() {
    std::scoped_lock lock(mutex);
    std::vector<pa_stream_state_t> list;
    for (const auto& [state, thread] : states)
      list.push_back(state);
    return list;
  }

  std::size_t startedCount() {
    std::scoped_lock lock(mutex);
    return started.size();
  }

  std::size_t underflowCount() {
    std::scoped_lock lock(mutex);
    return underflows.size();
  }
};

inline Seen& seen(void* userdata) { return *static_cast<Seen*>(userdata); }

inline void onState(pa_stream* s, void* userdata) {
  const auto state = pa_stream_get_state(s);
  std::scoped_lock lock(seen(userdata).mutex);
  seen(userdata).states.emplace_back(state, threadName());
}

inline void onStarted(pa_stream*, void* userdata) {
  std::scoped_lock lock(seen(userdata).mutex);
  seen(userdata).started.push_back(threadName());
}

inline void onUnderflow(pa_stream*, void* userdata) {
  std::scoped_lock lock(seen(userdata).mutex);
  seen(userdata).underflows.push_back(threadName());
}

inline void onSuccess(pa_stream*, int success, void* userdata) {
  std::scoped_lock lock(seen(userdata).mutex);
  seen(userdata).successes.emplace_back(success, threadName());
}

inline void onInfo(pa_context*, const pa_sink_input_info* info, int, void* userdata) {
  if (info == nullptr)
    return;
  std::scoped_lock lock(seen(userdata).mutex);
  seen(userdata).infos.push_back(
      {info->index, info->sample_spec, std::vector<pa_volume_t>(info->volume.values, info->volume.values + info->volume.channels)});
}

// a threaded mainloop and a context connected to a listener
class Client {
 public:
  // f() under the mainloop lock, as a threaded client calls the api
  template <typename F>
  auto locked(F f) -> decltype(f()) {
    pa_threaded_mainloop_lock(m_);
    if constexpr (std::is_void_v<decltype(f())>) {
      f();
      pa_threaded_mainloop_unlock(m_);
    } else {
      auto result = f();
      pa_threaded_mainloop_unlock(m_);
      return result;
    }
  }

  explicit Client(const Listener& listener) {
    m_ = pa_threaded_mainloop_new();
    REQUIRE(m_ != nullptr);
    REQUIRE(pa_threaded_mainloop_start(m_) == 0);
    const auto server = listener.server();
    const int connected = locked([&] {
      c_ = pa_context_new(pa_threaded_mainloop_get_api(m_), "test");
      return c_ == nullptr ? -1 : pa_context_connect(c_, server.c_str(), PA_CONTEXT_NOFLAGS, nullptr);
    });
    REQUIRE(connected == 0);
  }

  ~Client() {
    locked([&] {
      pa_context_disconnect(c_);
      pa_context_unref(c_);
    });
    pa_threaded_mainloop_stop(m_);
    pa_threaded_mainloop_free(m_);
  }

  Client(const Client&) = delete;
  Client& operator=(const Client&) = delete;

  pa_stream* stream(Seen& seen, pa_sample_format_t format, std::uint32_t rate, std::uint8_t channels) {
    const pa_sample_spec spec{format, rate, channels};
    auto* s = locked([&] {
      auto* made = pa_stream_new(c_, "music", &spec, nullptr);
      if (made != nullptr) {
        pa_stream_set_state_callback(made, onState, seen.ptr());
        pa_stream_set_started_callback(made, onStarted, seen.ptr());
        pa_stream_set_underflow_callback(made, onUnderflow, seen.ptr());
      }
      return made;
    });
    REQUIRE(s != nullptr);
    return s;
  }

  int connect(pa_stream* s, const pa_buffer_attr* attr, pa_stream_flags_t flags) {
    return locked([&] { return pa_stream_connect_playback(s, nullptr, attr, flags, nullptr, nullptr); });
  }

  // a stream reaches READY through its state callback, as a threaded client waits for it
  void ready(pa_stream* s) {
    REQUIRE(eventually([&] { return state(s) == PA_STREAM_READY; }));
  }

  pa_stream_state_t state(pa_stream* s) {
    return locked([&] { return pa_stream_get_state(s); });
  }

  pa_context_state_t contextState() {
    return locked([&] { return pa_context_get_state(c_); });
  }

  void complete(pa_operation* op) {
    REQUIRE(op != nullptr);
    REQUIRE(eventually([&] { return locked([&] { return pa_operation_get_state(op); }) != PA_OPERATION_RUNNING; }));
    CHECK(pa_operation_get_state(op) == PA_OPERATION_DONE);
    pa_operation_unref(op);
  }

  // an update completes on the loop after every job posted before it, the shim's started and underflow included
  void roundTrip(pa_stream* s) {
    complete(locked([&] { return pa_stream_update_timing_info(s, nullptr, nullptr); }));
  }

  // the read index of a fresh timing snapshot, or -1 without one
  std::int64_t readIndex(pa_stream* s) {
    roundTrip(s);
    return locked([&] {
      const auto* timing = pa_stream_get_timing_info(s);
      return timing == nullptr ? std::int64_t{-1} : timing->read_index;
    });
  }

  pa_timing_info freshTiming(pa_stream* s) {
    roundTrip(s);
    return locked([&] { return *pa_stream_get_timing_info(s); });
  }

  std::size_t writable(pa_stream* s) {
    return locked([&] { return pa_stream_writable_size(s); });
  }

  int write(pa_stream* s, const Bytes& bytes) {
    return locked([&] { return pa_stream_write(s, bytes.data(), bytes.size(), nullptr, 0, PA_SEEK_RELATIVE); });
  }

  int error() {
    return locked([&] { return pa_context_errno(c_); });
  }

  int corked(pa_stream* s) {
    return locked([&] { return pa_stream_is_corked(s); });
  }

  void cork(pa_stream* s, bool on, Seen& seen) {
    complete(locked([&] { return pa_stream_cork(s, on ? 1 : 0, onSuccess, seen.ptr()); }));
  }

  void flush(pa_stream* s, Seen& seen) {
    complete(locked([&] { return pa_stream_flush(s, onSuccess, seen.ptr()); }));
  }

  void finish(pa_stream* s) {
    locked([&] {
      pa_stream_disconnect(s);
      pa_stream_unref(s);
    });
  }

  [[nodiscard]] pa_threaded_mainloop* mainloop() const { return m_; }
  [[nodiscard]] pa_context* context() const { return c_; }

 private:
  pa_threaded_mainloop* m_ = nullptr;
  pa_context* c_ = nullptr;
};

}  // namespace cadence::test_support::pulse

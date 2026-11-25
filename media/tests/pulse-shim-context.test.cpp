// the shim's context on the pcm socket, its fake sink input and the helpers, through the c api alone
#include "pulse-shim/abi.h"
#include "support/pulse-client.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/time.h>

#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <ctime>
#include <mutex>
#include <optional>
#include <set>
#include <string>
#include <tuple>
#include <utility>
#include <vector>

using namespace cadence::test_support;
using namespace cadence::test_support::pulse;

namespace {

// index, average volume and channels of a reported sink input, or none at the end of the list; and eol
using Info = std::pair<std::optional<std::tuple<std::uint32_t, pa_volume_t, std::uint8_t>>, int>;

struct ContextSeen {
  std::mutex mutex;
  std::vector<pa_context_state_t> states;
  std::vector<int> successes;
  std::vector<std::pair<pa_subscription_event_type_t, std::uint32_t>> events;
  std::vector<Info> infos;

  template <typename T>
  T copy(const T& field) {
    std::scoped_lock lock(mutex);
    return field;
  }
};

ContextSeen& seenBy(void* userdata) { return *static_cast<ContextSeen*>(userdata); }

void onContextState(pa_context* c, void* userdata) {
  const auto state = pa_context_get_state(c);
  std::scoped_lock lock(seenBy(userdata).mutex);
  seenBy(userdata).states.push_back(state);
}

void onContextSuccess(pa_context*, int success, void* userdata) {
  std::scoped_lock lock(seenBy(userdata).mutex);
  seenBy(userdata).successes.push_back(success);
}

void onEvent(pa_context*, pa_subscription_event_type_t t, std::uint32_t idx, void* userdata) {
  std::scoped_lock lock(seenBy(userdata).mutex);
  seenBy(userdata).events.emplace_back(t, idx);
}

void onContextInfo(pa_context*, const pa_sink_input_info* info, int eol, void* userdata) {
  Info seen{std::nullopt, eol};
  if (info != nullptr)
    seen.first = std::tuple{info->index, pa_cvolume_avg(&info->volume), info->volume.channels};
  std::scoped_lock lock(seenBy(userdata).mutex);
  seenBy(userdata).infos.push_back(seen);
}

// a loop and an unconnected context whose state changes are recorded
class Unconnected {
 public:
  explicit Unconnected(ContextSeen& seen) {
    m_ = pa_threaded_mainloop_new();
    REQUIRE(pa_threaded_mainloop_start(m_) == 0);
    pa_threaded_mainloop_lock(m_);
    c_ = pa_context_new(pa_threaded_mainloop_get_api(m_), "test");
    REQUIRE(c_ != nullptr);
    CHECK(pa_context_get_state(c_) == PA_CONTEXT_UNCONNECTED);
    pa_context_set_state_callback(c_, onContextState, &seen);
    pa_threaded_mainloop_unlock(m_);
  }

  ~Unconnected() {
    pa_threaded_mainloop_lock(m_);
    if (c_ != nullptr) {
      pa_context_disconnect(c_);
      pa_context_unref(c_);
    }
    pa_threaded_mainloop_unlock(m_);
    pa_threaded_mainloop_stop(m_);
    pa_threaded_mainloop_free(m_);
  }

  Unconnected(const Unconnected&) = delete;
  Unconnected& operator=(const Unconnected&) = delete;

  int connect(const std::string& server) {
    pa_threaded_mainloop_lock(m_);
    const int result = pa_context_connect(c_, server.c_str(), PA_CONTEXT_NOFLAGS, nullptr);
    pa_threaded_mainloop_unlock(m_);
    return result;
  }

  // the caller holds the lock, as the threaded mainloop documentation prescribes
  void complete(pa_operation* op) {
    REQUIRE(op != nullptr);
    while (pa_operation_get_state(op) == PA_OPERATION_RUNNING)
      pa_threaded_mainloop_wait(m_);
    CHECK(pa_operation_get_state(op) == PA_OPERATION_DONE);
    pa_operation_unref(op);
  }

  pa_threaded_mainloop* m_ = nullptr;
  pa_context* c_ = nullptr;
};

}  // namespace

TEST_CASE("connect reaches ready before it returns") {
  Listener listener;
  ContextSeen seen;
  Unconnected client(seen);
  pa_threaded_mainloop_lock(client.m_);
  const auto server = listener.server();
  const int result = pa_context_connect(client.c_, server.c_str(), PA_CONTEXT_NOFLAGS, nullptr);
  const auto state = pa_context_get_state(client.c_);
  pa_threaded_mainloop_unlock(client.m_);
  CHECK(result == 0);
  CHECK(state == PA_CONTEXT_READY);
  CHECK(seen.copy(seen.states) == std::vector<pa_context_state_t>{PA_CONTEXT_CONNECTING, PA_CONTEXT_READY});
  listener.accept();
}

TEST_CASE("a missing socket fails the context with connection refused") {
  TempDir dir;
  ContextSeen seen;
  Unconnected client(seen);
  CHECK(client.connect("unix:" + dir.file("missing.sock")) < 0);
  pa_threaded_mainloop_lock(client.m_);
  CHECK(pa_context_get_state(client.c_) == PA_CONTEXT_FAILED);
  CHECK(pa_context_errno(client.c_) == PA_ERR_CONNECTIONREFUSED);
  // a context connects once
  CHECK(pa_context_connect(client.c_, nullptr, PA_CONTEXT_NOFLAGS, nullptr) < 0);
  CHECK(pa_context_errno(client.c_) == PA_ERR_BADSTATE);
  pa_threaded_mainloop_unlock(client.m_);
  CHECK(seen.copy(seen.states) == std::vector<pa_context_state_t>{PA_CONTEXT_CONNECTING, PA_CONTEXT_FAILED});
}

TEST_CASE("the sink input volume is stored and reported, never applied: nothing goes to the server") {
  Listener listener;
  ContextSeen seen;
  Unconnected client(seen);
  REQUIRE(client.connect(listener.server()) == 0);
  const auto server = listener.accept();
  const pa_volume_t thirty = PA_VOLUME_NORM * 30 / 100;
  pa_cvolume volume{};
  REQUIRE(pa_cvolume_set(&volume, 2, thirty) == &volume);
  pa_threaded_mainloop_lock(client.m_);
  pa_context_set_subscribe_callback(client.c_, onEvent, &seen);
  client.complete(pa_context_subscribe(client.c_, PA_SUBSCRIPTION_MASK_SINK_INPUT, onContextSuccess, &seen));
  client.complete(pa_context_set_sink_input_volume(client.c_, 7, &volume, onContextSuccess, &seen));
  // the same volume again changes nothing, so no event
  client.complete(pa_context_set_sink_input_volume(client.c_, 7, &volume, onContextSuccess, &seen));
  client.complete(pa_context_get_sink_input_info(client.c_, 7, onContextInfo, &seen));
  // three channels is no volume for a mono or stereo stream
  pa_cvolume three{};
  REQUIRE(pa_cvolume_set(&three, 3, thirty) == &three);
  CHECK(pa_context_set_sink_input_volume(client.c_, 7, &three, nullptr, nullptr) == nullptr);
  CHECK(pa_context_errno(client.c_) == PA_ERR_INVALID);
  pa_threaded_mainloop_unlock(client.m_);

  CHECK(seen.copy(seen.successes) == std::vector<int>{1, 1, 1});
  CHECK(seen.copy(seen.events) == std::vector<std::pair<pa_subscription_event_type_t, std::uint32_t>>{
                                      {PA_SUBSCRIPTION_EVENT_SINK_INPUT | PA_SUBSCRIPTION_EVENT_CHANGE, 7}});
  CHECK(seen.copy(seen.infos) ==
        std::vector<Info>{{std::tuple{std::uint32_t{7}, thirty, std::uint8_t{2}}, 0}, {std::nullopt, 1}});
  CHECK(server.idle());
}

TEST_CASE("a subscription without the sink input facility hears no volume change") {
  Listener listener;
  ContextSeen seen;
  Unconnected client(seen);
  REQUIRE(client.connect(listener.server()) == 0);
  const auto server = listener.accept();
  pa_cvolume volume{};
  REQUIRE(pa_cvolume_set(&volume, 1, PA_VOLUME_NORM / 2) == &volume);
  pa_threaded_mainloop_lock(client.m_);
  pa_context_set_subscribe_callback(client.c_, onEvent, &seen);
  client.complete(pa_context_subscribe(client.c_, PA_SUBSCRIPTION_MASK_NULL, onContextSuccess, &seen));
  client.complete(pa_context_set_sink_input_volume(client.c_, 3, &volume, onContextSuccess, &seen));
  pa_threaded_mainloop_unlock(client.m_);
  CHECK(seen.copy(seen.successes) == std::vector<int>{1, 1});
  CHECK(seen.copy(seen.events).empty());
}

TEST_CASE("disconnect is terminated and closes the socket; operations then fail") {
  Listener listener;
  ContextSeen seen;
  Unconnected client(seen);
  REQUIRE(client.connect(listener.server()) == 0);
  auto server = listener.accept();
  pa_threaded_mainloop_lock(client.m_);
  pa_context_disconnect(client.c_);
  CHECK(pa_context_get_state(client.c_) == PA_CONTEXT_TERMINATED);
  CHECK(pa_context_subscribe(client.c_, PA_SUBSCRIPTION_MASK_ALL, nullptr, nullptr) == nullptr);
  CHECK(pa_context_errno(client.c_) == PA_ERR_BADSTATE);
  pa_context_unref(client.c_);
  client.c_ = nullptr;
  pa_threaded_mainloop_unlock(client.m_);
  CHECK(seen.copy(seen.states).back() == PA_CONTEXT_TERMINATED);
  CHECK(server.readSome() == 0);
}

TEST_CASE("an operation on a context that failed meanwhile is cancelled") {
  Listener listener;
  ContextSeen seen;
  Unconnected client(seen);
  REQUIRE(client.connect(listener.server()) == 0);
  pa_threaded_mainloop_lock(client.m_);
  auto* op = pa_context_subscribe(client.c_, PA_SUBSCRIPTION_MASK_ALL, onContextSuccess, &seen);
  REQUIRE(op != nullptr);
  pa_context_disconnect(client.c_);
  pa_threaded_mainloop_unlock(client.m_);
  REQUIRE(eventually([&] { return pa_operation_get_state(op) != PA_OPERATION_RUNNING; }));
  CHECK(pa_operation_get_state(op) == PA_OPERATION_CANCELLED);
  pa_operation_unref(op);
  CHECK(seen.copy(seen.successes).empty());
}

TEST_CASE("strerror has a distinct text for every code and one for an unknown code") {
  std::set<std::string> texts;
  for (int code = PA_OK; code < PA_ERR_MAX; ++code) {
    const char* text = pa_strerror(code);
    REQUIRE(text != nullptr);
    CHECK(std::strlen(text) > 0);
    texts.insert(text);
  }
  CHECK(texts.size() == static_cast<std::size_t>(PA_ERR_MAX));
  CHECK(std::string(pa_strerror(PA_ERR_CONNECTIONREFUSED)) == "Connection refused");
  CHECK(std::string(pa_strerror(PA_ERR_NOTSUPPORTED)) == "Operation not supported");
  CHECK(pa_strerror(PA_ERR_MAX) != nullptr);
  CHECK(pa_strerror(-1) != nullptr);
}

TEST_CASE("cvolume_set fills the channels and cvolume_avg averages them") {
  pa_cvolume volume{};
  CHECK(pa_cvolume_set(&volume, 2, 1000) == &volume);
  CHECK(volume.channels == 2);
  CHECK(volume.values[0] == 1000);
  CHECK(volume.values[1] == 1000);
  CHECK(pa_cvolume_avg(&volume) == 1000);
  volume.values[1] = 2001;
  CHECK(pa_cvolume_avg(&volume) == 1500);
  CHECK(pa_cvolume_set(&volume, 33, 1000) == nullptr);
  CHECK(pa_cvolume_set(&volume, 0, 1000) == nullptr);
  CHECK(pa_cvolume_set(&volume, 2, PA_VOLUME_MAX + 1) == nullptr);
  CHECK(pa_cvolume_avg(nullptr) == PA_VOLUME_MUTED);
}

TEST_CASE("gettimeofday is the wall clock and timeval_diff the distance either way") {
  timeval now{};
  CHECK(pa_gettimeofday(&now) == &now);
  const auto wall = std::time(nullptr);
  CHECK(std::abs(static_cast<long>(now.tv_sec - wall)) <= 1);
  CHECK(pa_gettimeofday(nullptr) == nullptr);
  const timeval a{2, 500000};
  const timeval b{1, 750000};
  CHECK(pa_timeval_diff(&a, &b) == 750000);
  CHECK(pa_timeval_diff(&b, &a) == 750000);
  CHECK(pa_timeval_diff(&a, nullptr) == 0);
}

TEST_CASE("proplist_sets takes ascii keys and utf-8 values") {
  auto* p = pa_proplist_new();
  REQUIRE(p != nullptr);
  CHECK(pa_proplist_sets(p, "media.role", "music") == 0);
  CHECK(pa_proplist_sets(p, "media.role", "video") == 0);
  CHECK(pa_proplist_sets(p, "media.name", "caf\xc3\xa9") == 0);
  CHECK(pa_proplist_sets(p, "media.name", "\xff") < 0);
  CHECK(pa_proplist_sets(p, "", "x") < 0);
  CHECK(pa_proplist_sets(p, nullptr, "x") < 0);
  CHECK(pa_proplist_sets(p, "k\x01", "x") < 0);
  CHECK(pa_proplist_sets(nullptr, "k", "x") < 0);
  pa_proplist_free(p);
  pa_proplist_free(nullptr);
}

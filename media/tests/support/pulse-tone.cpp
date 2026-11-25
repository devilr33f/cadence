// pulse-tone <socket> <ms>: plays ms of a 1 kHz tone at half scale, s16 stereo at 44.1 kHz, through the libpulse shim
// to the pcm server at socket, the way a threaded client does, with its sink input turned down to 30 %, then waits
// until the server has read all of it. exits 0 once it has; 1 for bad arguments, 2 when the context or stream fails,
// 3 when the server never read it all
#include "pulse-shim/abi.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <numbers>
#include <string>
#include <thread>
#include <vector>

namespace {

constexpr std::uint32_t kRate = 44100;
constexpr std::size_t kFrameBytes = 4;

void signalOnState(pa_stream*, void* m) { pa_threaded_mainloop_signal(static_cast<pa_threaded_mainloop*>(m), 0); }

std::vector<std::int16_t> tone(std::size_t frames) {
  std::vector<std::int16_t> samples;
  for (std::size_t i = 0; i < frames; ++i) {
    const auto value = static_cast<std::int16_t>(
        std::lround(16384 * std::sin(2 * std::numbers::pi * 1000 * static_cast<double>(i) / kRate)));
    samples.push_back(value);
    samples.push_back(value);
  }
  return samples;
}

int play(pa_threaded_mainloop* m, const std::string& server, std::size_t ms) {
  pa_threaded_mainloop_lock(m);
  auto* c = pa_context_new(pa_threaded_mainloop_get_api(m), "pulse-tone");
  if (c == nullptr || pa_context_connect(c, server.c_str(), PA_CONTEXT_NOFLAGS, nullptr) != 0) {
    pa_threaded_mainloop_unlock(m);
    return 2;
  }
  const pa_sample_spec spec{PA_SAMPLE_S16LE, kRate, 2};
  auto* s = pa_stream_new(c, "tone", &spec, nullptr);
  pa_stream_set_state_callback(s, signalOnState, m);
  if (pa_stream_connect_playback(s, nullptr, nullptr, PA_STREAM_AUTO_TIMING_UPDATE, nullptr, nullptr) != 0) {
    pa_threaded_mainloop_unlock(m);
    return 2;
  }
  while (pa_stream_get_state(s) == PA_STREAM_CREATING)
    pa_threaded_mainloop_wait(m);
  if (pa_stream_get_state(s) != PA_STREAM_READY) {
    pa_threaded_mainloop_unlock(m);
    return 2;
  }
  // a client turns its own sink input down, as soloist follows its volume slider: the shim stores it, never applies it
  pa_cvolume volume{};
  pa_cvolume_set(&volume, 2, PA_VOLUME_NORM * 30 / 100);
  auto* op = pa_context_set_sink_input_volume(c, pa_stream_get_index(s), &volume, nullptr, nullptr);
  while (op != nullptr && pa_operation_get_state(op) == PA_OPERATION_RUNNING)
    pa_threaded_mainloop_wait(m);
  if (op == nullptr || pa_operation_get_state(op) != PA_OPERATION_DONE) {
    pa_threaded_mainloop_unlock(m);
    return 2;
  }
  pa_operation_unref(op);
  pa_threaded_mainloop_unlock(m);

  // the shim has no write callback: a client polls the room
  const auto samples = tone(kRate * ms / 1000);
  const auto* bytes = reinterpret_cast<const std::uint8_t*>(samples.data());
  const std::size_t total = samples.size() * sizeof(std::int16_t);
  std::size_t written = 0;
  while (written < total) {
    pa_threaded_mainloop_lock(m);
    const auto room = pa_stream_writable_size(s);
    if (room == SIZE_MAX) {
      pa_threaded_mainloop_unlock(m);
      return 2;
    }
    const auto n = std::min(room, total - written) / kFrameBytes * kFrameBytes;
    if (n > 0 && pa_stream_write(s, bytes + written, n, nullptr, 0, PA_SEEK_RELATIVE) == 0)
      written += n;
    pa_threaded_mainloop_unlock(m);
    if (n == 0)
      std::this_thread::sleep_for(std::chrono::milliseconds(5));
  }

  const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(10);
  for (;;) {
    pa_threaded_mainloop_lock(m);
    const auto* timing = pa_stream_get_timing_info(s);
    const bool drained = timing != nullptr && timing->read_index >= timing->write_index;
    pa_threaded_mainloop_unlock(m);
    if (drained)
      break;
    if (std::chrono::steady_clock::now() > deadline)
      return 3;
    std::this_thread::sleep_for(std::chrono::milliseconds(5));
  }

  pa_threaded_mainloop_lock(m);
  pa_stream_disconnect(s);
  pa_stream_unref(s);
  pa_context_disconnect(c);
  pa_context_unref(c);
  pa_threaded_mainloop_unlock(m);
  return 0;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc != 3)
    return 1;
  const auto ms = std::strtoul(argv[2], nullptr, 10);
  auto* m = pa_threaded_mainloop_new();
  if (m == nullptr || pa_threaded_mainloop_start(m) != 0)
    return 2;
  const int result = play(m, std::string("unix:") + argv[1], ms);
  pa_threaded_mainloop_stop(m);
  pa_threaded_mainloop_free(m);
  return result;
}

#include "backend/pcm-input.h"
#include "support/audiod-client.h"
#include "support/eventually.h"
#include "support/media-client.h"
#include "support/playback-client.h"
#include "support/signals.h"
#include "support/url-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <span>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr double kFullScale = 32768;
// 20 ms: whole cycles of the 1 kHz music and the 300 Hz overlay
constexpr std::size_t kWindow = 960;
constexpr double kMusic = 0.25;

// pushes in 100 ms pieces from its own thread, as a decoder would
std::thread feed(PcmInput& input, std::vector<float> stereo) {
  return std::thread([&input, stereo = std::move(stereo)] {
    for (std::size_t first = 0; first < stereo.size(); first += 4800 * 2)
      input.push(std::span<const float>(stereo).subspan(first, std::min<std::size_t>(4800 * 2, stereo.size() - first)),
                 48000, 2);
  });
}

std::vector<double> levels(const std::vector<std::int16_t>& mono, double hz) {
  std::vector<double> out;
  for (std::size_t first = 0; first + kWindow <= mono.size(); first += kWindow)
    out.push_back(amplitudeAt(mono, 1, 0, first, kWindow, hz) / kFullScale);
  return out;
}

}  // namespace

// M4b-D10: an overlay url plays once over the music, which ducks under it; no session, no MediaState
TEST_CASE("OverlayPlaysWithoutSession") {
  UrlRig rig;
  rig.server().route("/chime.wav", {.contentType = "audio/wav", .body = toneWav(1.0, 48000, 300, 300, 0.25)});
  auto client = rig.connect();
  const int fd = client.get();
  auto& music = rig.audiod().playback().music();

  auto producer = feed(music, sine(1000, kMusic, 48 * 4000, 2));
  std::this_thread::sleep_for(600ms);
  sendFrame(fd, urlPlay("chime", rig.server().url("/chime.wav"), kOverlayMode));
  auto frames = until(fd, [](const Frames& got) { return hasResult(got, "chime"); });
  expectResult(frames, "chime", "");
  CHECK(frames.states.empty());
  producer.join();
  // nothing about a session, before or after the overlay ended
  CHECK(until(fd, [](const Frames&) { return false; }, 2000ms).states.empty());
  CHECK(rig.audiod().logs().count(event("media_session_start")) == 0);
  CHECK(rig.audiod().logs().eventually(R"("msg":"media_overlay_end","reason":"ended","src":"o-1")"));
  CHECK(rig.audiod().logs().count(R"("msg":"url_open","src":"o-1",)") == 1);
  disconnect(rig.audiod(), client);

  const auto mix = firstChannel(readWav(rig.mix()));
  const auto overlay = levels(mix, 300);
  const auto musicLevels = levels(mix, 1000);
  std::vector<std::size_t> sounding;
  for (std::size_t window = 0; window < overlay.size(); ++window)
    if (overlay[window] > 0.2)
      sounding.push_back(window);
  REQUIRE(sounding.size() >= 45);
  CHECK(sounding.size() <= 55);
  // the music at its own level before, ducked to 0.15 of it while the overlay sounded, and back after the hold
  const auto first = sounding.front();
  const auto last = sounding.back();
  CHECK(std::abs(db(musicLevels[first - 10] / kMusic)) < 1);
  for (std::size_t window = first + 11; window <= last; ++window) {
    INFO(window);
    CHECK(std::abs(db(musicLevels[window] / (kMusic * 0.15))) < 1.5);
  }
  REQUIRE(last + 60 < musicLevels.size());
  CHECK(std::abs(db(musicLevels[last + 60] / kMusic)) < 1);
}

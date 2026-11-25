// the keyword spotter's calls and wake policy on a scripted model, and once on the tflm runner with the tiny model
// (kws/make_tiny_model.py). in 20 ms pushes, frame f after a (re-)arm ends at sample 640 + 160 f, so call c (from 1)
// ends at sample 480 + 1280 c, inside push 4 c + 2
#include "kws/spotter.h"
#include "kws/tflm-model.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <array>
#include <cstddef>
#include <cstdint>
#include <fstream>
#include <functional>
#include <iterator>
#include <memory>
#include <optional>
#include <span>
#include <string_view>
#include <utility>
#include <vector>

using namespace cadence;
using namespace cadence::kws;

namespace {

constexpr std::size_t kPush = 320;
constexpr std::size_t kCallFloats = KwsModel::kFrames * KwsModel::kMels;

// what the scripted model answers and what it was asked: the test keeps it while the spotter owns the model
struct Script {
  // call from 1; an empty answer fails the call
  std::function<std::optional<Outputs>(std::size_t call)> answer;
  std::vector<std::array<float, kCallFloats>> inputs;
};

class ScriptedModel final : public KwsModel {
 public:
  explicit ScriptedModel(std::shared_ptr<Script> script) : script_(std::move(script)) {}

  Result<Outputs, std::string_view> invoke(std::span<const float, kFrames * kMels> features) override {
    auto& input = script_->inputs.emplace_back();
    std::copy(features.begin(), features.end(), input.begin());
    if (const auto out = script_->answer(script_->inputs.size()))
      return *out;
    return std::string_view("invoke_failed");
  }

 private:
  std::shared_ptr<Script> script_;
};

// the keyword at pos and the intent at sp in every frame of a call
Outputs everyFrame(float pos, float sp) {
  Outputs out;
  for (std::size_t t = 0; t < KwsModel::kFrames; ++t) {
    out.posNeg[2 * t] = 1 - pos;
    out.posNeg[2 * t + 1] = pos;
    out.intUnint[2 * t] = 1 - sp;
    out.intUnint[2 * t + 1] = sp;
  }
  return out;
}

Outputs quiet() { return everyFrame(0.01f, 0.5f); }

Outputs peakAt(std::size_t frame, float pos, float sp, Outputs out = quiet()) {
  out.posNeg[2 * frame] = 1 - pos;
  out.posNeg[2 * frame + 1] = pos;
  out.intUnint[2 * frame] = 1 - sp;
  out.intUnint[2 * frame + 1] = sp;
  return out;
}

std::vector<std::int16_t> noise(std::size_t count, std::uint32_t seed) {
  std::vector<std::int16_t> samples(count);
  std::uint32_t x = seed;
  for (auto& sample : samples) {
    x = 1664525u * x + 1013904223u;
    sample = static_cast<std::int16_t>(static_cast<std::int32_t>(x >> 20) - 2048);
  }
  return samples;
}

constexpr SpotterPolicy kNoRearm{KWS_POS_THRESHOLD, KWS_SP_THRESHOLD, 1, 0};

struct Rig {
  explicit Rig(SpotterPolicy policy = SpotterPolicy::defaultCapsula(), std::uint32_t seed = 7)
      : script(std::make_shared<Script>()), audio(noise(400 * kPush, seed)),
        spotter(std::make_unique<ScriptedModel>(script), KwsFrontend(FrontendParams::defaultCapsula()), policy) {
    script->answer = [](std::size_t) { return quiet(); };
  }

  // push number `pushed + 1`, at 20 ms per push
  std::optional<Detection> push() {
    const std::span<const std::int16_t> all(audio);
    const auto block = all.subspan((pushed % 400) * kPush, kPush);
    ++pushed;
    return spotter.push(block, static_cast<std::int64_t>(pushed) * 20);
  }

  // pushes until (and including) push `last`, returning every push that detected
  std::vector<std::size_t> pushUntil(std::size_t last) {
    std::vector<std::size_t> detected;
    while (pushed < last)
      if (push())
        detected.push_back(pushed);
    return detected;
  }

  std::shared_ptr<Script> script;
  std::vector<std::int16_t> audio;
  KwsSpotter spotter;
  std::size_t pushed = 0;
};

constexpr std::size_t pushOfCall(std::size_t call) { return 4 * call + 2; }

}  // namespace

TEST_CASE("the default policy is the offline gate's") {
  const auto policy = SpotterPolicy::defaultCapsula();
  CHECK(KWS_POS_THRESHOLD == 0.999f);
  CHECK(KWS_SP_THRESHOLD == 0.2f);
  CHECK(KWS_CONSECUTIVE_CALLS == 1);
  CHECK(KWS_REARM_FRAMES == 147);
  CHECK(policy.posThreshold == KWS_POS_THRESHOLD);
  CHECK(policy.spThreshold == KWS_SP_THRESHOLD);
  CHECK(policy.consecutiveCalls == KWS_CONSECUTIVE_CALLS);
  CHECK(policy.rearmFrames == KWS_REARM_FRAMES);
}

TEST_CASE("spotter runs the model every 8 frames") {
  Rig rig;
  std::vector<std::size_t> callsAfter;
  for (std::size_t p = 1; p <= 50; ++p) {
    rig.push();
    callsAfter.push_back(rig.script->inputs.size());
  }
  for (std::size_t p = 1; p <= 50; ++p) {
    INFO("push " << p);
    std::size_t calls = 0;
    while (calls < 12 && pushOfCall(calls + 1) <= p)
      ++calls;
    CHECK(callsAfter[p - 1] == calls);
  }

  SECTION("each call takes the frontend's next 8 frames, oldest first, mel index fastest") {
    KwsFrontend frontend(FrontendParams::defaultCapsula());
    std::vector<KwsFrontend::Frame> frames;
    for (const auto& frame : frontend.push(std::span<const std::int16_t>(rig.audio).subspan(0, 50 * kPush)))
      frames.push_back(frame);
    REQUIRE(rig.script->inputs.size() == 12);
    for (std::size_t call = 0; call < 12; ++call)
      for (std::size_t t = 0; t < KwsModel::kFrames; ++t)
        for (std::size_t m = 0; m < KwsModel::kMels; ++m)
          REQUIRE(rig.script->inputs[call][t * KwsModel::kMels + m] == frames[call * 8 + t][m]);
  }

  SECTION("1280-sample pushes queue 5 frames, then call once a push with 5 left over") {
    Rig stock;
    const std::span<const std::int16_t> all(stock.audio);
    std::vector<std::size_t> calls;
    for (std::size_t f = 0; f < 4; ++f) {
      stock.spotter.push(all.subspan(f * 1280, 1280), 0);
      calls.push_back(stock.script->inputs.size());
    }
    CHECK(calls == std::vector<std::size_t>{0, 1, 2, 3});
  }
}

TEST_CASE("spotter fires once per threshold crossing and honours cooldown") {
  SECTION("a held keyword fires once after the warm-up, then once each rearm after that") {
    Rig rig;
    rig.script->answer = [](std::size_t) { return everyFrame(1.0f, 1.0f); };
    // calls 1-18 count 144 frames and score none; call 19 scores its last 5
    CHECK(rig.pushUntil(pushOfCall(19) - 1).empty());
    CHECK(rig.script->inputs.size() == 18);
    const auto first = rig.push();
    REQUIRE(first);
    CHECK(first->score == 1.0f);
    CHECK(first->atMs == static_cast<std::int64_t>(pushOfCall(19)) * 20);

    // the detection dropped the rest of its push and re-armed: the next 18 calls score nothing again
    const std::size_t woke = rig.pushed;
    CHECK(rig.pushUntil(woke + pushOfCall(19) - 1).empty());
    CHECK(rig.script->inputs.size() == 19 + 18);
    const auto second = rig.push();
    REQUIRE(second);
    CHECK(second->atMs == static_cast<std::int64_t>(woke + pushOfCall(19)) * 20);
  }

  SECTION("one crossing fires once") {
    Rig rig(kNoRearm);
    rig.script->answer = [](std::size_t call) { return call == 3 ? everyFrame(1.0f, 1.0f) : quiet(); };
    CHECK(rig.pushUntil(100) == std::vector<std::size_t>{pushOfCall(3)});
  }

  SECTION("the warm-up ends at frame 147") {
    // call 19 holds frames 144 to 151
    Rig early;
    early.script->answer = [](std::size_t call) { return call == 19 ? peakAt(2, 1.0f, 1.0f) : quiet(); };
    CHECK(early.pushUntil(pushOfCall(25)).empty());

    Rig scored;
    scored.script->answer = [](std::size_t call) { return call == 19 ? peakAt(3, 1.0f, 1.0f) : quiet(); };
    CHECK(scored.pushUntil(pushOfCall(25)) == std::vector<std::size_t>{pushOfCall(19)});
  }

  SECTION("a call fires on the keyword's peak and the intent at its first frame") {
    struct Case {
      const char* name;
      Outputs out;
      bool fires;
    };
    Outputs indexZero = quiet();
    indexZero.posNeg[6] = 1.0f;
    indexZero.posNeg[7] = 0.0f;
    const Case cases[] = {
        {"both thresholds met exactly", peakAt(3, 0.999f, 0.2f), true},
        {"keyword just below", peakAt(3, 0.9989f, 1.0f), false},
        {"intent just below", peakAt(3, 1.0f, 0.19f), false},
        {"the intent of the first frame at the peak", peakAt(5, 1.0f, 0.9f, peakAt(2, 1.0f, 0.1f)), false},
        {"the intent of the peak, not a lower frame", peakAt(5, 1.0f, 0.9f, peakAt(2, 0.9995f, 0.1f)), true},
        {"index 0 is not the keyword", indexZero, false},
    };
    for (const auto& c : cases) {
      INFO(c.name);
      Rig rig(kNoRearm);
      const Outputs out = c.out;
      rig.script->answer = [out](std::size_t call) { return call == 1 ? out : quiet(); };
      const auto detected = rig.pushUntil(pushOfCall(3));
      CHECK(detected.size() == (c.fires ? 1u : 0u));
    }
  }

  SECTION("consecutive calls must each fire, and a failed call breaks the run") {
    Rig rig(SpotterPolicy{KWS_POS_THRESHOLD, KWS_SP_THRESHOLD, 2, 0});
    // 1 fires, 2 does not, 3 fires, 4 fails, 5 and 6 fire
    rig.script->answer = [](std::size_t call) -> std::optional<Outputs> {
      if (call == 4)
        return std::nullopt;
      if (call == 2 || call > 6)
        return quiet();
      return everyFrame(1.0f, 1.0f);
    };
    CHECK(rig.pushUntil(pushOfCall(10)) == std::vector<std::size_t>{pushOfCall(6)});
  }
}

TEST_CASE("spotter reset clears state") {
  SECTION("the queued frames and the frontend's samples go: the next call is 8 fresh frames") {
    Rig rig(kNoRearm);
    // call 1 at push 6, then 5 frames queued after push 8
    rig.pushUntil(8);
    REQUIRE(rig.script->inputs.size() == 1);
    rig.spotter.reset();
    rig.pushUntil(8 + pushOfCall(1) - 1);
    CHECK(rig.script->inputs.size() == 1);
    rig.push();
    REQUIRE(rig.script->inputs.size() == 2);

    KwsFrontend fresh(FrontendParams::defaultCapsula());
    const auto frames = fresh.push(std::span<const std::int16_t>(rig.audio).subspan(8 * kPush, pushOfCall(1) * kPush));
    REQUIRE(frames.size() >= KwsModel::kFrames);
    for (std::size_t t = 0; t < KwsModel::kFrames; ++t)
      for (std::size_t m = 0; m < KwsModel::kMels; ++m)
        REQUIRE(rig.script->inputs[1][t * KwsModel::kMels + m] == frames[t][m]);
  }

  SECTION("the warm-up starts again") {
    Rig rig;
    rig.script->answer = [](std::size_t) { return everyFrame(1.0f, 1.0f); };
    CHECK(rig.pushUntil(pushOfCall(18)).empty());
    rig.spotter.reset();
    const std::size_t reset = rig.pushed;
    CHECK(rig.pushUntil(reset + pushOfCall(19)) == std::vector<std::size_t>{reset + pushOfCall(19)});
  }

  SECTION("a run of firing calls breaks") {
    Rig rig(SpotterPolicy{KWS_POS_THRESHOLD, KWS_SP_THRESHOLD, 2, 0});
    rig.script->answer = [](std::size_t) { return everyFrame(1.0f, 1.0f); };
    rig.pushUntil(pushOfCall(1));
    REQUIRE(rig.script->inputs.size() == 1);
    rig.spotter.reset();
    const std::size_t reset = rig.pushed;
    CHECK(rig.pushUntil(reset + pushOfCall(2)) == std::vector<std::size_t>{reset + pushOfCall(2)});
  }
}

TEST_CASE("spotter drives the tflm runner") {
  std::ifstream in(CADENCE_KWS_TINY_MODEL, std::ios::binary);
  REQUIRE(in);
  const std::vector<std::uint8_t> bytes{std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
  auto made = TflmModel::load(bytes, 256 * 1024);
  REQUIRE(made.ok());

  // any score fires, so the first call past the warm-up detects
  KwsSpotter spotter(std::move(made.value()), KwsFrontend(FrontendParams::defaultCapsula()),
                     SpotterPolicy{0.0f, 0.0f, 1, KWS_REARM_FRAMES});
  const auto audio = noise(pushOfCall(19) * kPush, 9);
  const std::span<const std::int16_t> all(audio);
  std::vector<std::size_t> detected;
  for (std::size_t p = 1; p <= pushOfCall(19); ++p)
    if (const auto detection = spotter.push(all.subspan((p - 1) * kPush, kPush), static_cast<std::int64_t>(p)))
      detected.push_back(p);
  CHECK(detected == std::vector<std::size_t>{pushOfCall(19)});
}

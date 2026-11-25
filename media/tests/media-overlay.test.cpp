#include "audiod/media-handlers.h"
#include "audiod/media-source.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/fake-media-source.h"
#include "support/media-client.h"
#include "support/playback-client.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

using Calls = std::vector<std::string>;

// audiod with fake url sources on the file backend and a hardware volume control
class Rig {
 public:
  explicit Rig(std::chrono::milliseconds playBound = kUrlPlayBound)
      : volume_(std::make_shared<FakeVolume>()),
        audiod_(compiledBackends(), {}, withFakeControl(volume_), openCapture,
                HarnessMedia{.factory = media_.factory(), .playBound = playBound}) {}

  [[nodiscard]] FakeMedia& media() { return media_; }
  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }

  UniqueFd connect() {
    auto client = connectConfigured(audiod_, audiod_.file("out"), "Master");
    expectApplied(client.get(), 40, true);
    return client;
  }

  template <typename Report>
  void on(const std::string& name, Report report) {
    const bool found = audiod_.onLoop([&](IpcServer&, Daemon&) {
      auto* source = media_.source(name);
      if (source != nullptr)
        report(*source);
      return source != nullptr;
    });
    REQUIRE(found);
  }

 private:
  FakeMedia media_;
  std::shared_ptr<FakeVolume> volume_;
  AudiodHarness audiod_;
};

// sends an overlay request the factory takes: its answer waits for the source
void overlay(int fd, std::string_view requestId, std::string_view url) {
  sendFrame(fd, urlPlay(requestId, url, kOverlayMode));
  const auto frames = untilPong(fd);
  CHECK(frames.results.empty());
  CHECK(frames.states.empty());
}

}  // namespace

TEST_CASE("an overlay plays on the overlay input, answered once its audio starts, and starts no session") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();

  overlay(fd, "o", "http://fake/o");
  PcmInput* opened = nullptr;
  rig.on("o", [&](FakeMedia::Source& source) { opened = source.music(); });
  CHECK(opened == &rig.audiod().playback().overlay());

  rig.on("o", [](FakeMedia::Source& source) {
    source.events().metadata({"Title", "Artist", {}, {}, {}, {}});
    source.events().duration(2000);
    source.events().playing();
  });
  auto frames = untilPong(fd);
  REQUIRE(frames.results.size() == 1);
  expectResult(frames, "o", "");
  CHECK(frames.states.empty());

  rig.on("o", [](FakeMedia::Source& source) { source.events().ended(); });
  frames = untilPong(fd);
  CHECK(frames.results.empty());
  CHECK(frames.states.empty());
  CHECK(rig.media().calls("o") == Calls{"made", "open", "destroyed"});
  CHECK(rig.audiod().logs().count(R"("msg":"media_overlay_end","reason":"ended")") == 1);
  CHECK(rig.audiod().logs().count(R"("msg":"media_command","action":"play","mode":"overlay","code":"ok")") == 1);
  CHECK(rig.audiod().logs().count(event("media_session_start")) == 0);
}

TEST_CASE("an overlay that fails or ends before it played answers why") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();

  overlay(fd, "f", "http://fake/f");
  rig.on("f", [](FakeMedia::Source& source) { source.events().failed("not_found"); });
  expectResult(untilPong(fd), "f", "not_found");
  CHECK(rig.media().calls("f") == Calls{"made", "open", "destroyed"});
  CHECK(rig.audiod().logs().count(R"("msg":"media_overlay_end","reason":"failed","code":"not_found")") == 1);

  overlay(fd, "e", "http://fake/e");
  rig.on("e", [](FakeMedia::Source& source) { source.events().ended(); });
  expectResult(untilPong(fd), "e", "failed");

  answered(fd, urlPlay("r", "http://fake/refused/unsupported", kOverlayMode), "r", "unsupported");
}

TEST_CASE("a newer overlay replaces the one before and cuts what it queued") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  auto& input = rig.audiod().playback().overlay();

  overlay(fd, "a", "http://fake/a");
  // a was still on its way: its request is answered for it
  sendFrame(fd, urlPlay("b", "http://fake/b", kOverlayMode));
  auto frames = untilPong(fd);
  REQUIRE(frames.results.size() == 1);
  expectResult(frames, "a", "replaced");
  const auto journal = rig.media().journal();
  const auto at = [&](std::string_view entry) {
    return std::find(journal.begin(), journal.end(), entry) - journal.begin();
  };
  CHECK(at("a:stop") < at("a:destroyed"));
  CHECK(at("a:destroyed") < at("b:open"));
  CHECK(rig.audiod().logs().count(R"("msg":"media_overlay_end","reason":"replaced")") == 1);

  rig.on("b", [](FakeMedia::Source& source) { source.events().playing(); });
  frames = untilPong(fd);
  REQUIRE(frames.results.size() == 1);
  expectResult(frames, "b", "");

  // c replaces b, which played: b's queued audio is cut and nothing more is answered for b
  const std::vector<float> queued(4800 * 2, 0.25f);
  REQUIRE(input.push(std::span<const float>(queued), 48000, 2));
  overlay(fd, "c", "http://fake/c");
  CHECK(input.status().queued == 0);
  CHECK(rig.media().calls("b") == Calls{"made", "open", "stop", "destroyed"});
}

TEST_CASE("the gate stops an overlay, and refuses one while it is shut") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();

  overlay(fd, "o", "http://fake/o");
  sendFrame(fd, volumeSet(101));
  const auto frames = untilPong(fd);
  expectResult(frames, "o", "refused");
  CHECK(rig.media().calls("o") == Calls{"made", "open", "stop", "destroyed"});
  CHECK(rig.audiod().logs().count(R"("msg":"media_overlay_end","reason":"gated")") == 1);

  answered(fd, urlPlay("shut", "http://fake/p", kOverlayMode), "shut", "refused");
  CHECK(rig.media().calls("p").empty());
}

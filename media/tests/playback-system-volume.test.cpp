#include "audiod/output.h"
#include "audiod/system-volume.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/playback-client.h"

#include <catch2/catch_test_macros.hpp>

#include <mutex>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;

namespace {

// android's STREAM_MUSIC as the app's hook sees it, read on audiod's loop thread and moved by the test's
struct Stream {
  std::mutex mu;
  int index = 5;
  int max = 15;
  bool muted = false;
  std::vector<int> writes;

  void move(int to, bool mute = false) {
    const std::lock_guard lock(mu);
    index = to;
    muted = mute;
  }
  std::vector<int> written() {
    const std::lock_guard lock(mu);
    return writes;
  }
};

int readStream(void* ctx, int* index, int* max, int* muted) {
  auto* s = static_cast<Stream*>(ctx);
  const std::lock_guard lock(s->mu);
  *index = s->index;
  *max = s->max;
  *muted = s->muted ? 1 : 0;
  return 0;
}

int writeStream(void* ctx, int index) {
  auto* s = static_cast<Stream*>(ctx);
  const std::lock_guard lock(s->mu);
  s->writes.push_back(index);
  s->index = index;
  return 0;
}

// the file backend, with the system volume as its control when the configuration says so (aaudio's own path)
OutputFactory withSystemVolume() {
  return [](const OutputConfig& config) {
    auto opened = audiod::openOutput(config);
    if (opened.ok() && config.system_volume)
      opened.value().volume = SystemVolumeControl::open();
    return opened;
  };
}

}  // namespace

// one level: Ready says where the stream stands, cadenced's VolumeSet moves it (and nothing when it holds the
// level), audiod's gain stays at unity, and a move the system made itself goes to cadenced, whose echo writes nothing
TEST_CASE("the system volume is the level") {
  Stream stream;
  const cadence_system_volume_backend_t backend{&stream, readStream, writeStream};
  cadence_audiod_system_volume(&backend);
  {
    AudiodHarness audiod(compiledBackends(), {}, withSystemVolume());
    auto client = connectTo(audiod.socket());
    auto configure = configureOutput(audiod.file("out"));
    configure.body.configure.output.system_volume = true;
    sendFrame(client.get(), configure);
    const auto ready = expectReady(client.get());
    REQUIRE(ready.has_system_volume);
    CHECK(ready.system_volume.level == 33);
    CHECK_FALSE(ready.system_volume.muted);
    CHECK(audiod.logs().count(event("playback_opened")) == 1);
    CHECK(audiod.logs().count(R"("volume":"system")") == 1);

    // 30 is the stream's index 5 already: the gate opens and nothing is written
    expectApplied(client.get(), 30, true);
    CHECK(stream.written().empty());
    expectApplied(client.get(), 20, true);
    CHECK(stream.written() == std::vector<int>{3});

    // the remote's key, android's own: reported, and cadenced's echo writes nothing back
    stream.move(9);
    cadence_audiod_system_volume_changed();
    const auto moved = expectFrame(client.get(), cadence_audiod_v1_Envelope_system_volume_tag).body.system_volume;
    CHECK(moved.level == 60);
    CHECK_FALSE(moved.muted);
    expectApplied(client.get(), 60, true);
    CHECK(stream.written() == std::vector<int>{3});

    // a mute alone
    stream.move(9, true);
    cadence_audiod_system_volume_changed();
    const auto muted = expectFrame(client.get(), cadence_audiod_v1_Envelope_system_volume_tag).body.system_volume;
    CHECK(muted.level == 60);
    CHECK(muted.muted);
  }
  cadence_audiod_system_volume(nullptr);
}

TEST_CASE("without the hook the level stays a software gain and Ready carries no system volume") {
  cadence_audiod_system_volume(nullptr);
  AudiodHarness audiod(compiledBackends(), {}, withSystemVolume());
  auto client = connectTo(audiod.socket());
  auto configure = configureOutput(audiod.file("out"));
  configure.body.configure.output.system_volume = true;
  sendFrame(client.get(), configure);
  CHECK_FALSE(expectReady(client.get()).has_system_volume);
  CHECK(audiod.logs().count(R"("volume":"software")") == 1);
}

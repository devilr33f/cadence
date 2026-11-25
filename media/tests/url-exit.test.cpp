#include "support/audiod-child.h"
#include "support/audiod-client.h"
#include "support/eventually.h"
#include "support/http-server.h"
#include "support/media-client.h"
#include "support/playback-client.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators_range.hpp>

#include <signal.h>
#include <sys/wait.h>

#include <chrono>
#include <string>
#include <thread>

using namespace cadence;
using namespace cadence::test_support;
using namespace std::chrono_literals;

// the decoder outlives the loop by up to one interrupt poll: a SIGTERM while it waits in a tls handshake must still
// end in a clean exit, not in openssl torn down under it
TEST_CASE("ExitsOnSigtermWhileATlsHandshakeWaits") {
  const auto round = GENERATE(range(0, 4));
  INFO(round);
  // a plain http server reads the client hello as a request that never ends: the handshake waits
  HttpServer silent;
  TempDir dir;
  const auto socket = dir.file("audiod.sock");
  Child audiod({"--socket", socket});
  audiod::UniqueFd client;
  REQUIRE(eventually([&] {
    client = tryConnect(socket);
    return client.valid();
  }));
  sendFrame(client.get(), configureOutput(dir.file("out")));
  expectReady(client.get());
  expectApplied(client.get(), 40, true);
  auto url = silent.url("/stream");
  url.replace(0, 4, "https");
  sendFrame(client.get(), urlPlay("play", url));
  CHECK(untilPong(client.get()).results.empty());
  std::this_thread::sleep_for(150ms);

  audiod.signal(SIGTERM);
  const int status = audiod.wait();
  CHECK(WIFEXITED(status));
  CHECK(WEXITSTATUS(status) == 0);
}

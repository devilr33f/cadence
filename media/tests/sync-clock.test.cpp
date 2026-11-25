#include "sync/clock-client.h"
#include "sync/clock-estimator.h"
#include "sync/clock-map.h"
#include "sync/clock-server.h"
#include "sync/clock-wire.h"
#include "sync/sha256.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cmath>
#include <random>
#include <string_view>
#include <thread>

using namespace cadence::sync;

namespace {

std::span<const std::uint8_t> bytes(std::string_view s) {
  return {reinterpret_cast<const std::uint8_t*>(s.data()), s.size()};
}

std::string hex(std::span<const std::uint8_t> d) {
  static constexpr char kHex[] = "0123456789abcdef";
  std::string out;
  for (const auto b : d) {
    out += kHex[b >> 4];
    out += kHex[b & 15];
  }
  return out;
}

const std::vector<std::uint8_t> kKey(32, 0x5a);

// a leader clock and a lan: leader = member * (1 + skew) + offset; each leg takes base plus exponential jitter, a
// share of legs finding the air idle (fast), the uplink alone spiking (power save: the asymmetric case the fastest-
// exchange filter exists for)
struct SimNet {
  std::mt19937_64 rng;
  double offsetNs = 0;
  double skew = 0;
  double baseNs = 0;
  double jitterNs = 0;
  double fastP = 0;
  double fastJitterNs = 0;
  double spikeP = 0;
  double spikeNs = 0;
  double upExtraNs = 0;
  double lossP = 0;
  double turnaroundNs = 20'000;

  double leader(double member) const { return member * (1 + skew) + offsetNs; }

  double leg(bool up) {
    std::uniform_real_distribution<double> u(0, 1);
    const double mean = u(rng) < fastP ? fastJitterNs : jitterNs;
    double d = baseNs + (mean > 0 ? std::exponential_distribution<double>(1 / mean)(rng) : 0);
    if (up) {
      d += upExtraNs;
      if (u(rng) < spikeP)
        d += u(rng) * spikeNs;
    }
    return d;
  }

  std::optional<ClockSample> exchange(double t1) {
    if (std::uniform_real_distribution<double>(0, 1)(rng) < lossP)
      return std::nullopt;
    const double arrive = t1 + leg(true);
    const double t2 = leader(arrive);
    const double t3 = t2 + turnaroundNs;
    const double t4 = arrive + turnaroundNs / (1 + skew) + leg(false);
    return ClockSample{static_cast<std::int64_t>(t1), static_cast<std::int64_t>(t2), static_cast<std::int64_t>(t3),
                       static_cast<std::int64_t>(t4)};
  }
};

struct SimResult {
  ClockEstimate estimate;
  double errorNs = 0;
};

SimResult runSim(SimNet& net, double seconds) {
  ClockEstimator est;
  const double start = 5e9;
  const double end = start + seconds * 1e9;
  for (double t1 = start; t1 < end; t1 += 100e6)
    if (const auto s = net.exchange(t1))
      est.add(*s);
  const auto at = static_cast<std::int64_t>(end);
  const auto e = est.estimate(at);
  REQUIRE(e);
  const double truth = net.leader(static_cast<double>(at)) - static_cast<double>(at);
  return {*e, e->offsetNs - truth};
}

}  // namespace

TEST_CASE("sha-256 and hmac-sha256 against the published vectors") {
  CHECK(hex(sha256(bytes(""))) == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
  CHECK(hex(sha256(bytes("abc"))) == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  CHECK(hex(sha256(bytes("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq"))) ==
        "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1");
  // rfc 4231 cases 1 and 2, and 6 (a key longer than the block)
  const std::vector<std::uint8_t> key1(20, 0x0b);
  CHECK(hex(hmacSha256(key1, bytes("Hi There"))) ==
        "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7");
  CHECK(hex(hmacSha256(bytes("Jefe"), bytes("what do ya want for nothing?"))) ==
        "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843");
  const std::vector<std::uint8_t> key6(131, 0xaa);
  CHECK(hex(hmacSha256(key6, bytes("Test Using Larger Than Block-Size Key - Hash Key First"))) ==
        "60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54");
}

TEST_CASE("the clock's packets round-trip and refuse what the key did not make") {
  const auto req = encodeRequest(kKey, ClockRequest{.seq = 9, .t1 = -5});
  const auto got = decodeRequest(kKey, req);
  REQUIRE(got);
  CHECK(got->seq == 9);
  CHECK(got->t1 == -5);

  const auto rep = encodeReply(kKey, ClockReply{.seq = 7, .t1 = 1, .t2 = 2'000'000'000'123, .t3 = -3});
  const auto r = decodeReply(kKey, rep);
  REQUIRE(r);
  CHECK(r->t2 == 2'000'000'000'123);
  CHECK(r->t3 == -3);

  auto tampered = req;
  tampered[10] ^= 1;
  CHECK_FALSE(decodeRequest(kKey, tampered));
  const std::vector<std::uint8_t> other(32, 0x11);
  CHECK_FALSE(decodeRequest(other, req));
  // a request is no reply, and a cut packet is nothing
  CHECK_FALSE(decodeReply(kKey, req));
  CHECK_FALSE(decodeRequest(kKey, std::span<const std::uint8_t>(req.data(), req.size() - 1)));
}

TEST_CASE("busy wi-fi: the fastest exchanges hold the offset within 50 us and the skew within 5 ppm") {
  SimNet net{.rng = std::mt19937_64(1), .offsetNs = 1.234e9, .skew = 80e-6, .baseNs = 1.5e6, .jitterNs = 3e6,
             .fastP = 0.33, .fastJitterNs = 100e3, .spikeP = 0.2, .spikeNs = 80e6, .lossP = 0.05};
  const auto r = runSim(net, 60);
  CHECK(std::abs(r.errorNs) < 50e3);
  CHECK(std::abs(r.estimate.skew - 80e-6) < 5e-6);
  CHECK(r.estimate.splitNs < 50e3);
}

TEST_CASE("a quiet lan: within 10 us") {
  SimNet net{.rng = std::mt19937_64(3), .offsetNs = -42e9, .skew = -30e-6, .baseNs = 200e3, .jitterNs = 50e3};
  const auto r = runSim(net, 30);
  CHECK(std::abs(r.errorNs) < 10e3);
  CHECK(r.estimate.splitNs < 10e3);
}

TEST_CASE("hopeless air: the estimate degrades but the split figure says so") {
  SimNet net{.rng = std::mt19937_64(1), .offsetNs = 1.234e9, .skew = 80e-6, .baseNs = 1.5e6, .jitterNs = 2e6,
             .spikeP = 0.2, .spikeNs = 80e6, .lossP = 0.05};
  const auto r = runSim(net, 60);
  CHECK(std::abs(r.errorNs) < 1e6);
  // never claims pair precision it does not have
  CHECK(r.estimate.splitNs > 20e3);
}

TEST_CASE("a constant asymmetry is a bias no two-way exchange sees: half of it lands in the offset") {
  SimNet net{.rng = std::mt19937_64(5), .baseNs = 300e3, .jitterNs = 20e3, .upExtraNs = 400e3};
  const auto r = runSim(net, 30);
  CHECK(std::abs(r.errorNs - 200e3) < 20e3);
}

TEST_CASE("an exchange with a broken stamp is dropped, and the window lets old samples go") {
  ClockEstimator est({.windowNs = 1'000'000'000, .keep = 0.5, .minSamples = 2});
  est.add({.t1 = 10, .t2 = 0, .t3 = 100, .t4 = 20});
  CHECK(est.size() == 0);
  est.add({.t1 = 0, .t2 = 10, .t3 = 10, .t4 = 20});
  est.add({.t1 = 2'000'000'000, .t2 = 2'000'000'010, .t3 = 2'000'000'010, .t4 = 2'000'000'020});
  CHECK(est.size() == 1);
  CHECK_FALSE(est.estimate(2'000'000'020));
}

TEST_CASE("a mapping turns leader time into local time and back") {
  const ClockMapping m{.offsetNs = 1.5e9, .skew = 100e-6, .atNs = 10'000'000'000};
  for (const std::int64_t local : {10'000'000'000LL, 0LL, 50'000'000'000LL, 3'600'000'000'000LL}) {
    const auto leader = m.toLeader(local);
    CHECK(std::llabs(m.toLocal(leader) - local) <= 1);
  }
  // at at, the leader is ahead by the offset alone; 1 s later, by 100 us more
  CHECK(m.toLeader(10'000'000'000) == 11'500'000'000);
  CHECK(m.toLeader(11'000'000'000) == 12'500'100'000);
}

TEST_CASE("the published mapping slews toward a new estimate, and steps only past the threshold") {
  ClockSmoother s({.maxSlew = 50e-6, .stepNs = 5e6, .syncedBelowNs = 100e3, .minSpanNs = 3'000'000'000});
  ClockEstimate e{.at = 0, .offsetNs = 1000, .skew = 0, .splitNs = 10e3, .spanNs = 10'000'000'000};
  auto m = s.update(e);
  CHECK(m.offsetNs == 1000);
  CHECK(m.synced);
  CHECK(m.generation == 0);

  // 1 s later the estimate is 200 us off: only 50 us of it is taken
  e.at = 1'000'000'000;
  e.offsetNs = 201'000;
  m = s.update(e);
  CHECK(std::abs(m.offsetNs - 51'000) < 1);
  CHECK(m.generation == 0);

  // past 5 ms the mapping jumps, and says so by its generation
  e.at = 2'000'000'000;
  e.offsetNs = 10e6;
  m = s.update(e);
  CHECK(m.offsetNs == 10e6);
  CHECK(m.generation == 1);

  // an error over the budget, or a window too short, is published but not synced
  e.at = 3'000'000'000;
  e.splitNs = 200e3;
  CHECK_FALSE(s.update(e).synced);
  e.splitNs = 10e3;
  e.spanNs = 1'000'000'000;
  CHECK_FALSE(s.update(e).synced);
}

TEST_CASE("the shared mapping is none until stored and after a clear") {
  SharedClockMapping shared;
  CHECK_FALSE(shared.load());
  shared.store({.offsetNs = 5, .skew = 1e-6, .atNs = 7, .errorNs = 3, .generation = 2, .synced = true});
  const auto m = shared.load();
  REQUIRE(m);
  CHECK(m->offsetNs == 5);
  CHECK(m->generation == 2);
  CHECK(m->synced);
  shared.clear();
  CHECK_FALSE(shared.load());
}

TEST_CASE("a member syncs to a leader over loopback, and a stranger's key is refused") {
  auto serverSock = ClockSocket::open("127.0.0.1", 0);
  REQUIRE(serverSock.ok());
  ClockServer server(std::move(serverSock.value()), kKey);
  const auto port = server.port();
  REQUIRE(port != 0);
  server.start();

  const auto leader = ClockSocket::resolve("127.0.0.1", port);
  REQUIRE(leader);
  ClockClientConfig config;
  config.intervalNs = 10'000'000;
  config.smoother.minSpanNs = 1'000'000'000;
  auto clientSock = ClockSocket::open("127.0.0.1", 0);
  REQUIRE(clientSock.ok());
  ClockClient client(std::move(clientSock.value()), *leader, kKey, config);

  auto strangerSock = ClockSocket::open("127.0.0.1", 0);
  REQUIRE(strangerSock.ok());
  ClockClient stranger(std::move(strangerSock.value()), *leader, std::vector<std::uint8_t>(32, 0x01), config);

  client.start();
  stranger.start();
  std::optional<ClockMapping> m;
  for (int i = 0; i < 60 && !(m && m->synced); ++i) {
    std::this_thread::sleep_for(std::chrono::milliseconds(50));
    m = client.mapping().load();
  }
  stranger.stop();
  client.stop();
  server.stop();

  REQUIRE(m);
  CHECK(m->synced);
  // one machine, one clock: the offset is what our own stamping adds, well under a millisecond
  CHECK(std::abs(m->offsetNs) < 1e6);
  CHECK(std::abs(m->skew) < 1e-3);
  CHECK(client.stats().replies > 50);
  CHECK(server.stats().refused > 0);
  CHECK_FALSE(stranger.mapping().load());
#if defined(__linux__)
  // the kernel's receive stamps on both ends: what keeps the scheduler's wake-up out of the exchange
  CHECK(client.stats().kernelStamps);
  CHECK(server.stats().kernelStamps);
#endif
}

TEST_CASE("sim clocks: a skewed, offset member maps onto a skewed, offset leader") {
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 2'500'000'000, .ppm = 40, .startNs = start};
  const SyncClock memberClock{.offsetNs = -700'000'000, .ppm = -60, .startNs = start};
  CHECK(std::llabs(leaderClock.toMono(leaderClock.toSync(start + 123'456'789)) - (start + 123'456'789)) <= 1);

  auto serverSock = ClockSocket::open("127.0.0.1", 0);
  REQUIRE(serverSock.ok());
  ClockServer server(std::move(serverSock.value()), kKey);
  server.setClock(leaderClock);
  const auto leader = ClockSocket::resolve("127.0.0.1", server.port());
  REQUIRE(leader);
  server.start();
  ClockClientConfig config;
  config.intervalNs = 10'000'000;
  config.smoother.minSpanNs = 1'000'000'000;
  config.clock = memberClock;
  auto clientSock = ClockSocket::open("127.0.0.1", 0);
  REQUIRE(clientSock.ok());
  ClockClient client(std::move(clientSock.value()), *leader, kKey, config);
  client.start();
  std::optional<ClockMapping> m;
  for (int i = 0; i < 60 && !(m && m->synced); ++i) {
    std::this_thread::sleep_for(std::chrono::milliseconds(50));
    m = client.mapping().load();
  }
  client.stop();
  server.stop();
  REQUIRE(m);
  // a leader time, through the mapping and the member's inverse, lands on the mono time the leader meant
  const auto mono = monoNs() + 500'000'000;
  const auto heard = memberClock.toMono(m->toLocal(leaderClock.toSync(mono)));
  CHECK(std::llabs(heard - mono) < 1'000'000);
}

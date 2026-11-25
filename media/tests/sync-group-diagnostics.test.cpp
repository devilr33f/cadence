// a member's drop diagnostics, pure on an injected clock: the reason tokens its log lines carry, one line per cause per
// few seconds with the rest counted, and the summary's counts and figures
#include "sync/group-diagnostics.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::sync;

namespace {

constexpr std::int64_t kSecond = 1'000'000'000;

std::uint32_t countOf(const GroupSummary& s, GroupCause cause) { return s.counts[static_cast<std::size_t>(cause)]; }

}  // namespace

TEST_CASE("each cause has its own token") {
  CHECK(causeToken(GroupCause::Placement) == "placement");
  CHECK(causeToken(GroupCause::Underrun) == "underrun");
  CHECK(causeToken(GroupCause::ClockLost) == "clock_lost");
  CHECK(causeToken(GroupCause::ClockStep) == "clock_step");
  CHECK(causeToken(GroupCause::Gap) == "gap");
  CHECK(causeToken(GroupCause::Link) == "link");
  CHECK(causeToken(GroupCause::Lost) == "lost");
  CHECK(causeToken(GroupCause::Step) == "step");
  CHECK(causeToken(GroupCause::Unsynced) == "unsynced");
  CHECK(causeToken(GroupCause::Synced) == "synced");
}

TEST_CASE("a flapping cause logs once per 5 s, then says how many it held back; another cause is not held") {
  GroupDiagnostics d;
  std::uint64_t suppressed = 99;
  const std::int64_t t = 100 * kSecond;
  CHECK(d.admit(GroupCause::Placement, t, suppressed));
  CHECK(suppressed == 0);
  CHECK_FALSE(d.admit(GroupCause::Placement, t + kSecond, suppressed));
  CHECK_FALSE(d.admit(GroupCause::Placement, t + 4 * kSecond, suppressed));
  // another cause has its own limit
  CHECK(d.admit(GroupCause::Underrun, t + kSecond, suppressed));
  CHECK(suppressed == 0);
  CHECK(d.admit(GroupCause::Placement, t + 5 * kSecond, suppressed));
  CHECK(suppressed == 2);
  CHECK(d.admit(GroupCause::Placement, t + 11 * kSecond, suppressed));
  CHECK(suppressed == 0);
}

TEST_CASE("the summary every 30 s counts every cause, held back or not, and the clock's and output's figures") {
  GroupDiagnostics d;
  const std::int64_t t = 10 * kSecond;
  std::uint64_t suppressed = 0;
  d.restart(t);
  for (int i = 0; i < 27; ++i)
    d.admit(GroupCause::Placement, t + i * kSecond, suppressed);
  d.admit(GroupCause::Underrun, t, suppressed);
  d.admit(GroupCause::Underrun, t + kSecond, suppressed);
  d.admit(GroupCause::ClockLost, t + 2 * kSecond, suppressed);
  for (int i = 0; i < 100; ++i) {
    const auto now = t + std::int64_t{i} * 100'000'000;
    REQUIRE(d.sampleDue(now));
    d.sample(now, GroupSample{.mapped = true,
                              .clockErrorUs = static_cast<std::uint32_t>(i < 50 ? 1000 : 3900),
                              .rttMinUs = static_cast<std::uint32_t>(2000 + i),
                              .rttP50Us = 6000,
                              .playing = true,
                              .placementErrorUs = i == 70 ? -150'000 : 40,
                              .delayFrames = i == 80 ? 14'400 : 9'600});
    CHECK_FALSE(d.sampleDue(now + 50'000'000));
  }
  CHECK_FALSE(d.summary(t + 29 * kSecond));
  const auto s = d.summary(t + 30 * kSecond);
  REQUIRE(s);
  CHECK(s->periodMs == 30'000);
  CHECK(countOf(*s, GroupCause::Placement) == 27);
  CHECK(countOf(*s, GroupCause::Underrun) == 2);
  CHECK(countOf(*s, GroupCause::ClockLost) == 1);
  CHECK(countOf(*s, GroupCause::Step) == 0);
  CHECK(s->clockErrorP50Us == 3900);
  CHECK(s->clockErrorMaxUs == 3900);
  CHECK(s->rttMinUs == 2000);
  CHECK(s->rttP50Us == 6000);
  CHECK(s->placementErrorMaxUs == -150'000);
  // a delay that jumped 100 ms once
  CHECK(s->delayMinFrames == 9'600);
  CHECK(s->delayMaxFrames == 14'400);
  // the next window starts empty
  const auto next = d.summary(t + 60 * kSecond);
  REQUIRE(next);
  CHECK(countOf(*next, GroupCause::Placement) == 0);
  CHECK(next->clockErrorMaxUs == 0);
  CHECK(next->delayMaxFrames == 0);
}

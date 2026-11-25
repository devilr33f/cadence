#include "app/routes/system/tests/support.h"

using namespace cadence::screen;
using namespace cadence::screen::routes;
using namespace cadence::screen::routes::systest;

namespace {

std::string text(ui::preview::Harness& h, const char* id) {
  auto* e = h.ui().page()->GetElementById(id);
  return e != nullptr ? e->GetInnerRML() : "<none>";
}

}  // namespace

TEST_CASE("the claim against a fake ClaimState sequence: connecting, the code, its expiry, a fresh code, declined, approved") {
  auto& h = harness();
  auto& f = fresh(h);
  // no token: the first run, connecting
  f.deliver(syspreview::linkStatus(cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED));
  h.step(50);
  REQUIRE(f.system().enroll() != nullptr);
  auto& enroll = *f.system().enroll();
  CHECK(f.system().top() == cadence_v2_ScreenRoute_SCREEN_ROUTE_ENROLL);
  CHECK(enroll.phase() == Enroll::Phase::Connecting);
  CHECK(f.system().route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_ENROLL);

  // the claim: the code in two groups of three, the expiry line and its words
  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_PENDING, "K7M4QX", f.epoch() + (9 * 60 + 41) * 1000));
  h.step(50);
  CHECK(enroll.phase() == Enroll::Phase::Claim);
  CHECK(text(h, "ecode") == "K7M · 4QX");
  CHECK(text(h, "eexpiry").find("9:41") != std::string::npos);
  // the one focusable action
  CHECK(focusedId(h) == "e-have");
  h.step(1100);
  INFO(text(h, "eexpiry"));
  CHECK(text(h, "eexpiry").find("9:40") != std::string::npos);

  // at 0 cadenced opens a fresh one: it cross-fades in by itself
  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_EXPIRED, "K7M4QX", 0));
  h.step(50);
  CHECK(h.ui().page()->QuerySelector(".enotice") != nullptr);
  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_PENDING, "P9RT2W", f.epoch() + 600000));
  h.step(50);
  CHECK(text(h, "ecode") == "P9R · T2W");

  // declined in the dashboard: said so over the next code
  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_DECLINED, "P9RT2W", 0));
  h.step(50);
  REQUIRE(h.ui().page()->QuerySelector(".enotice") != nullptr);

  // approved: connected for 2 s, then home
  f.online();
  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_APPROVED, "", 0));
  h.step(100);
  CHECK(enroll.phase() == Enroll::Phase::Connected);
  CHECK(h.ui().page()->GetElementById("films-0") == nullptr);
  h.step(2100);
  CHECK(f.system().top() == cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  CHECK(h.ui().page()->GetElementById("films-0") != nullptr);
}

TEST_CASE("the fallback: the 8-character dashboard code over a keyboard of its alphabet") {
  auto& h = harness();
  auto& f = fresh(h);
  f.deliver(syspreview::linkStatus(cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED));
  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_PENDING, "K7M4QX", f.epoch() + 600000));
  h.step(50);
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(50);
  REQUIRE(f.system().enroll() != nullptr);
  auto& enroll = *f.system().enroll();
  REQUIRE(enroll.phase() == Enroll::Phase::Code);
  // no O, I, L or U on the keyboard
  CHECK(h.ui().page()->GetElementById("ek-O") == nullptr);
  CHECK(h.ui().page()->GetElementById("ek-I") == nullptr);
  for (const char* k : {"ek-A", "ek-B", "ek-C", "ek-D", "ek-2", "ek-3", "ek-4", "ek-5"}) {
    h.ui().focus().focus(h.ui().page()->GetElementById(k));
    h.press(cadence_screen_v1_Key_KEY_OK);
  }
  CHECK(enroll.entry() == "ABCD2345");
  // a ninth is not taken; delete takes one back
  h.ui().focus().focus(h.ui().page()->GetElementById("ek-Z"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  CHECK(enroll.entry() == "ABCD2345");
  // focus stays on the key it pressed
  CHECK(focusedId(h) == "ek-Z");
  h.ui().focus().focus(h.ui().page()->GetElementById("e-send"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  const auto* sent = f.link().last("enroll_code");
  REQUIRE(sent != nullptr);
  CHECK(sent->code == "ABCD2345");
  // BACK goes back to the claim
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(50);
  CHECK(enroll.phase() == Enroll::Phase::Claim);
}

TEST_CASE("unreachable, no network and removed are each the shared state block") {
  auto& h = harness();
  auto& f = fresh(h);
  f.deliver(syspreview::linkStatus(cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED));
  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_FAILED, "", 0, "unreachable"));
  h.step(50);
  CHECK(f.system().enroll()->phase() == Enroll::Phase::Unreachable);
  CHECK(h.ui().page()->GetElementById("e-retry") != nullptr);
  // «Изменить адрес»: the keyboard prefilled, saved as a ServerAddress
  f.system().device().serverUrl = "https://a";
  h.ui().focus().focus(h.ui().page()->GetElementById("e-address"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(50);
  CHECK(f.system().enroll()->phase() == Enroll::Phase::Address);
  h.ui().focus().focus(h.ui().page()->GetElementById("e-save"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  const auto* addr = f.link().last("server_address");
  REQUIRE(addr != nullptr);
  CHECK(addr->json == "https://a");

  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_FAILED, "", 0, "no_network"));
  h.step(50);
  CHECK(f.system().enroll()->phase() == Enroll::Phase::NoNetwork);

  f.deliver(syspreview::linkStatus(cadence_screen_v1_LinkState_LINK_STATE_REMOVED));
  h.step(50);
  CHECK(f.system().enroll()->phase() == Enroll::Phase::Removed);
  // then back to the claim
  f.deliver(syspreview::claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_PENDING, "K7M4QX", f.epoch() + 600000));
  h.step(50);
  CHECK(f.system().enroll()->phase() == Enroll::Phase::Claim);
}

#pragma once

#include "app/routes/core/router.h"
#include "app/routes/system/env.h"
#include "app/routes/system/state.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// enrollment and the first run (design r2 section 22): device-first. connecting with the server's address under it;
// the claim (a qr drawn on the device, the 6-character code in two groups of three in inter figures, its expiry as
// a shrinking cadence line and «код действует 9:41», a fresh code cross-fading in by itself); connected for 2 s,
// then home; the 8-character dashboard code as the fallback (8 boxes over a keyboard of the code's alphabet); and
// the error states: unreachable (the address, the reason, «Повторить», «Изменить адрес» with the keyboard
// prefilled), no network, declined or expired, removed from cadence. cadenced holds the claim (ClaimState); i3's
// layer opens the route and hands it the claim's messages
class Enroll final : public Route {
 public:
  enum class Phase { Connecting, Unreachable, NoNetwork, Claim, Connected, Removed, Code, Address };

  Enroll(Services& s, std::shared_ptr<SystemState> state);
  ~Enroll() override;

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_ENROLL; }
  std::string body() override { return build(); }
  void loaded(Rml::ElementDocument& doc, bool restoring) override;
  bool back() override;
  bool activate(const ui::Activation& a) override;
  [[nodiscard]] std::string bodyClasses() const override { return "page enroll"; }

  // claim_state and link_status; the system shows the route while it is wanted
  void message(const cadence_screen_v1_Envelope& m);
  [[nodiscard]] Phase phase() const { return phase_; }
  [[nodiscard]] bool wanted() const { return wanted_; }
  [[nodiscard]] const std::string& code() const { return code_; }
  [[nodiscard]] const std::string& entry() const { return entry_; }

  // the code as shown: «K7M · 4QX»
  static std::string grouped(std::string_view code);
  // the 8-character dashboard code's alphabet (server/internal/enroll's)
  static constexpr std::string_view kCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  static constexpr std::size_t kCodeLength = 8;

 private:
  std::string build();
  void set(Phase p);
  void done();
  void tick(std::int64_t nowMs);
  void refreshExpiry();
  std::string claimBody() const;
  std::string codeBody() const;
  std::string addressBody() const;
  std::string boxes() const;
  void refreshEntry();
  std::string notice() const;
  [[nodiscard]] Rml::Element* el(const char* id) const;

  std::shared_ptr<SystemState> state_;
  Env env_;
  Alive alive_;
  const DeviceState& device_;
  std::vector<int> hooks_;
  Rml::ElementDocument* doc_ = nullptr;
  Phase phase_ = Phase::Connecting;
  bool wanted_ = false;
  std::string code_;
  std::string url_;
  std::int64_t expiresMs_ = 0;
  std::int64_t issuedMs_ = 0;
  // declined, expired, removed: the line over the next code
  std::string why_;
  std::string error_;
  std::string entry_;
  std::string address_;
  bool latin_ = true;
  std::int64_t connectedMs_ = -1;
  std::int64_t now_ = 0;
  std::int64_t lastSecond_ = -1;
  bool freshCode_ = false;
};

}  // namespace cadence::screen::routes

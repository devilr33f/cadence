#pragma once

#include "app/routes/core/catalog.h"
#include "app/routes/core/failure.h"
#include "app/routes/core/play.h"
#include "app/routes/core/router.h"

#include <cstdint>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// resolving (design r2 section 18.5): a full-screen step over the title's backdrop (ink 85 %) with a small poster,
// the title, «Серия 13 · AniLibria · 1080p», and the steps as the server reaches them (media.play: the server
// resolves and plays it; {stage} events where a server streams them), each a spinner that becomes a check, an indeterminate
// cadence line under them and an elapsed counter after 5 s. BACK cancels (RpcCancel) and returns with focus on the
// element that started it. a failure replaces the steps with a triangle, the reason in words and «Другая озвучка
// или источник» (focused), «Повторить», «Назад». the session is cadenced's: its MediaPlay starts the player, and
// resolving stays until the player has a picture: past the server's stages it shows the player's own (its cache
// filling, «Буферизация 42 % · 2,4 МБ/с»), and on the first frame it hands over in one eased move: the backdrop
// grows and fades and the card lifts away while the film, already drawing under the page, shows through; only then
// does the player's page replace it, so no black frame comes between
class ResolvingRoute final : public Route {
 public:
  ResolvingRoute(Services& s, const RouteArgs& args);

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING; }
  // the film draws under the page once it starts: the page's own ink is the .resolving layer, which fades
  [[nodiscard]] std::string bodyClasses() const override { return "page over-video"; }
  std::string body() override;
  void loaded(Rml::ElementDocument& doc, bool restoring) override;
  void start() override;
  bool activate(const ui::Activation& a) override;
  bool back() override;
  void update(std::int64_t nowMs) override;
  void context(ScreenContextData& c) const override;
  void playerState(const player::PlayerState& state) override;

  // the session id the play went out with, "" before
  [[nodiscard]] const std::string& sessionId() const { return session_; }
  // tests: a resolve event as the server streams it
  void event(const Json& ev);
  // a step the server opened itself (media.video/prepare: a link played from the dashboard): no call of its own, it
  // waits for the play that follows (any new session) or the server's failure
  [[nodiscard]] bool external() const { return external_; }
  // the server named what it resolves (its title, channel and art), or failed to
  void prepared(const std::string& title, const std::string& label, const std::string& art);
  void prepareFailed(const std::string& code);

 private:
  // Starting: media.play answered, its MediaPlay not yet the player's; Loading: the player has the session and waits
  // for its first frame; Handing: the hand-over's motion runs
  enum class State { Resolving, Starting, Loading, Handing, Failed, Done };
  struct Step {
    std::string stage;
    bool done = false;
    std::int64_t remainingMs = -1;
    std::int64_t remainingAtMs = 0;
    int height = 0;
    std::string vcodec;
    std::string acodec;
    int audioTracks = 0;
    // a step not yet done, by its stage alone (the rest filled as its events come)
    static Step named(std::string name) {
      Step s;
      s.stage = std::move(name);
      return s;
    }
  };

  void resolve();
  // it failed: what failed as it came (failure.h)
  void fail(failure::Call why);
  // a failure of the screen's own making (a timeout it measured, the player's error)
  [[nodiscard]] failure::Call own(const char* method, const std::string& code, const std::string& failure,
                                  std::int64_t sinceMs) const;
  // media.play answered: the session it started
  void started(const std::string& sessionId);
  std::string stepsRml() const;
  std::string failureRml() const;
  void redraw();
  // the player's step while it loads: its words and its bar, set in place (no redraw: the spinner keeps turning)
  void loadingStep();
  // the first frame is there: the motion, then the player's page
  void handOver();
  void finish();

  play::Title title_;
  Json choices_;
  std::string label_;
  std::string from_;
  bool external_ = false;
  State state_ = State::Resolving;
  std::vector<Step> steps_;
  failure::Call failure_;
  // a retry under way: the failure stays shown, «Повторить» spinning, until the first stage, the play or a failure
  bool retrying_ = false;
  std::string callId_;
  std::string session_;
  // the player's session when the call went (a film playing under the browse)
  std::string before_;
  std::int64_t startedMs_ = 0;
  std::int64_t shownSeconds_ = -1;
  std::int64_t shownWaitSeconds_ = -1;
  std::int64_t playSentMs_ = -1;
  std::int64_t loadingSinceMs_ = -1;
  std::string loadingText_;
  int loadingPercent_ = -2;
};

}  // namespace cadence::screen::routes

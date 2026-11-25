#pragma once

#include "app/routes/core/catalog.h"
#include "app/routes/core/failure.h"
#include "app/routes/home/answer.h"
#include "app/routes/core/router.h"
#include "app/routes/system/nearby.h"

#include <cstdint>
#include <map>
#include <set>
#include <memory>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// home (design r2 section 11): the top bar, a hero that collapses into a preview of the focused item once focus
// leaves it, then each provider's rows in a fixed order (continue watching first). each provider loads on its own
// (catalog.browse {path: <provider>}): its header and a skeleton at once, rows below never wait for rows above, a
// provider that is down collapses to a strip (moved to the bottom after 3 failed retries), one that is not
// configured is left out. while home is on top every provider is asked again each minute, and at once on coming
// back to it (so a source catalogSources adds or drops, new torrents and continue-watching progress show), quietly:
// an answer like the last changes nothing, another updates that provider's rows in place, the focused card kept
// (by id) with each row's scroll, and an answer is held while focus moves (scrolling), until it rests
class HomeRoute final : public Route {
 public:
  HomeRoute(Services& s, const RouteArgs& args);

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME; }
  std::string body() override;
  void loaded(Rml::ElementDocument& doc, bool restoring) override;
  void start() override;
  bool activate(const ui::Activation& a) override;
  // its backdrop fills the page: no fill of the page's own under it
  [[nodiscard]] std::string bodyClasses() const override { return "page home-page"; }
  bool back() override;
  void update(std::int64_t nowMs) override;
  void online() override;
  [[nodiscard]] bool placesOwnFocus() const override { return playSig_.size() > 0; }

  // the providers home asks, in row order (section 11's table)
  static const std::vector<std::string>& providers();

  // the refresh interval; RouteArgs.data's refreshMs shortens it (tests)
  static constexpr std::int64_t kRefreshMs = 60000;
  // focus that moved within this long is scrolling: a refresh waits
  static constexpr std::int64_t kRestMs = 1500;
  // a hidden feed (not found, a forgotten ref) is asked again this rarely, not with every refresh
  static constexpr std::int64_t kRecheckMs = 10 * 60 * 1000;

 private:
  struct Section {
    std::string id;
    std::string title;
    std::string path;
    std::string next;
    std::vector<catalog::Item> items;
    bool cont = false;
    // cards in the dom: they grow as focus nears the end (horizontal virtualisation)
    std::size_t shown = 0;
    // later pages joined it (more:): a refresh keeps them after its first page
    bool extended = false;
  };
  enum class State { Loading, Slow, Ready, Down, Absent };
  struct Provider {
    std::string id;
    State state = State::Loading;
    std::vector<Section> sections;
    int failures = 0;
    std::int64_t askedMs = 0;
    std::int64_t downSinceMs = 0;
    std::string callId;
    // the sections json last applied: a refresh answering the same changes nothing
    std::string sig;
    // a refresh's answer held while focus moves
    bool held = false;
    // its last failure as it came, and a retry under way (the failure stays shown, its button spinning)
    failure::Call failure;
    bool retrying = false;
    std::string heldStatus;
    Json heldJson;
    // no row for this household (home_answer::Hide): asked again no sooner than this; never (HideForGood): not again
    std::int64_t recheckAtMs = 0;
    bool never = false;
  };

  static std::vector<Section> parseSections(const Json& json);
  void load(Provider& p);
  // a down provider asked again: its failure stays until the answer
  void retry(Provider& p);
  void answered(const std::string& provider, const std::string& status, const Json& json);
  void refresh();
  void refreshed(Provider& p, const std::string& status, const Json& json);
  void hide(Provider& p, home_answer::Outcome how);
  // the provider's rows drawn again in place: focus, row scroll and row memory kept by id
  void redrawInPlace(const Provider& p);
  [[nodiscard]] bool resting() const;
  [[nodiscard]] std::string heroSig() const;
  std::string providerRml(const Provider& p);
  std::string sectionRml(const Provider& p, Section& s);
  void redrawProvider(const Provider& p);
  void redrawHero();
  void more(const std::string& rowId);
  void grow(Rml::Element* focused);
  void open(const catalog::Item& item);
  [[nodiscard]] const catalog::Item* hero() const;
  const catalog::Item* itemOf(const std::string& ref) const;
  Provider* provider(const std::string& id);
  void preview(const catalog::Item* item);
  // the rested card: its backdrop and its details (catalog.item, asked once per ref and kept)
  void settle();
  void loadDetail(const std::string& ref);
  // the details block for an item: the server's detail when it came, else nothing yet
  std::string detailRml(const catalog::Item& item) const;
  // the page's backdrop: art ("img:<id>", "gen:...", or empty for none) cross-fades in over the last once its image is
  // bound; instant places it at once (a page just loaded)
  void backdrop(const std::string& art, bool instant = false);
  void stepBackdrop(std::int64_t nowMs);
  // the feed column: the focused row in its place, the rows above it dimmed or hidden
  void placeRows(Rml::Element* row);
  // background playback: video keeps the hero's «Продолжить»; audio gets a compact chip in the top bar
  void followPlay();
  void redrawHeroNow();
  void redrawNowPlaying();
  // what plays on other devices (the system layer's devices.media feed): the strip of this room's, the top bar's
  // indicator of all, drawn again in place when the feed changes
  void followNearby();
  [[nodiscard]] std::vector<nearby::Item> nearbyItems() const;
  [[nodiscard]] std::string devicesIndicator() const;
  // the household's alarms (the system layer's alarms part): the next alarm and the running timers under the nearby
  // strip, drawn again when the summary changes, their countdowns ticked each second
  void followAlarms();
  [[nodiscard]] std::string alarmsStrip() const;

  std::vector<Provider> providers_;
  Rml::Element* lastFocus_ = nullptr;
  bool inRows_ = false;
  std::string restRef_;
  std::int64_t restSinceMs_ = -1;
  std::string previewRef_;
  std::string heroRef_;
  std::int64_t nowMs_ = 0;
  std::int64_t refreshMs_ = kRefreshMs;
  std::int64_t nextRefreshMs_ = -1;
  bool refreshDue_ = false;
  std::int64_t movedMs_ = -1;
  // focus set by the route itself after a redraw: not the viewer scrolling
  bool ownFocus_ = false;
  std::string heroSig_;
  // the backdrop's two layers (bg-0, bg-1): the art asked for, the one on top, one waiting for its image
  std::string bgArt_;
  // art focus asked for while a cross-fade ran: shown once it ends
  std::string bgWant_;
  // catalog.item's detail by ref (a few dozen kept), and the one being asked for
  std::map<std::string, catalog::Item> details_;
  std::string detailRef_;
  // refs catalog.item refused: not asked again
  std::set<std::string> noDetail_;
  std::string detailCall_;
  int bgTop_ = 0;
  int bgPending_ = -1;
  std::int64_t bgSwapMs_ = -1;
  // the background play's metadata and state ("" when none), and whether the hero currently offers a film
  std::string playSig_;
  bool heroHasPlay_ = false;
  // the feed's revision the strip and the indicator show
  std::uint64_t nearbyRev_ = 0;
  // the alarms summary's revision the strip shows, and the second its countdowns last showed
  std::uint64_t alarmsRev_ = 0;
  std::int64_t alarmsSecond_ = -1;
};

}  // namespace cadence::screen::routes

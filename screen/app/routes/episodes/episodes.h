#pragma once

#include "app/routes/core/failure.h"
#include "app/routes/core/catalog.h"
#include "app/routes/core/play.h"
#include "app/routes/core/router.h"

#include <cstdint>
#include <map>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// episodes (design r2 section 18.3): a grid of wide cards, 4 a row; an episode without a still is a numbered tile.
// fractional ordinals print as given and sort between their neighbours (the label is the key). over 50 episodes,
// range chips at the top («1–50 · 51–100 …», or the server's own groups, seasons among them), each remembering its
// focus. focus starts on the next unwatched episode. OK opens the picker, or plays at once with a remembered choice
class EpisodesRoute final : public Route {
 public:
  EpisodesRoute(Services& s, const RouteArgs& args);

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_EPISODES; }
  std::string body() override;
  void loaded(Rml::ElementDocument& doc, bool restoring) override;
  void start() override;
  bool activate(const ui::Activation& a) override;
  void update(std::int64_t nowMs) override;
  void context(ScreenContextData& c) const override;
  void online() override;

  // grid columns
  static constexpr std::size_t kColumns = 4;
  // a range chip holds this many when the server sends no groups of its own
  static constexpr std::size_t kRange = 50;

 private:
  enum class State { Loading, Ready, Failed };
  struct Range {
    std::string id;
    std::string label;
    // a server group (fetched with catalog.item {group}); else a slice of the loaded episodes
    bool server = false;
    std::size_t from = 0;
    std::size_t to = 0;
    std::string focused;
  };

  void load(const std::string& group, const std::string& cursor);
  void buildRanges();
  [[nodiscard]] std::vector<const catalog::Item*> visible() const;
  std::string gridRml() const;
  std::string chipsRml() const;
  void redrawGrid(bool focusNext);
  void play(const std::string& episode);
  [[nodiscard]] std::string nextUnwatched() const;
  [[nodiscard]] std::string focusedEpisode() const;

  play::Title title_;
  // the last failure as it came, a retry under way (the failure stays, «Повторить» spinning)
  failure::Call failure_;
  bool retrying_ = false;
  std::int64_t askedMs_ = 0;
  State state_ = State::Loading;
  catalog::Detail detail_;
  std::vector<catalog::Item> episodes_;
  std::vector<Range> ranges_;
  std::size_t range_ = 0;
  std::string next_;
  bool loadingMore_ = false;
  std::string pendingEpisode_;
  std::string optionsCall_;
};

}  // namespace cadence::screen::routes

#pragma once

#include "app/routes/panels/side-sheet.h"
#include "app/routes/playback/context.h"

#include <RmlUi/Core/ObserverPtr.h>

#include <array>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// the video osd's state machine (design §12), apart from rmlui so its keys can be walked in a test: shown or not,
// the row and the column focus is on (with each row's memory), the compact bar, auto-hide
struct OsdState {
  enum Row { Scrubber = 0, Transport = 1, Details = 2 };
  bool shown = false;
  Row row = Transport;
  // each row's remembered column
  std::array<int, 3> column{0, 2, 0};
  std::int64_t lastKeyMs = 0;
  std::int64_t compactUntilMs = 0;
  // a seek held on the scrubber, or ← → with the osd hidden: how far it went (accelerating)
  int seekRepeats = 0;
};

// the player route (SCREEN_ROUTE_PLAYER): the film owns the screen. the osd's three rows over the bottom scrim
// (scrubber; transport and the track chips; item actions and panel chips), subtitles drawn by the screen from mpv's
// text, the skip pill, up next with its countdown and auto-continue, buffering after 1 s, the error block, and the
// side sheets (panels, the queue, the voiceover switcher)
class PlayerPage final : public Page {
 public:
  PlayerPage(PlaybackContext& c, SideSheet& sheet);

  void enter() override;
  std::string body() override;
  void loaded(bool restoring) override;
  void leave() override;
  bool key(const platform::KeyEvent& event) override;
  bool activate(const ui::Activation& a) override;
  void tick(std::int64_t nowMs) override;
  void playerChanged() override;
  void subtitleChanged() override;
  void sessionChanged() override;
  [[nodiscard]] cadence_v2_ScreenRoute route() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER; }

  // shows the osd for ms (0: until the next auto-hide) with focus on row, column
  void show(OsdState::Row row, int column = -1, std::int64_t forMs = 0);
  void hide();
  [[nodiscard]] const OsdState& osd() const { return osd_; }
  // the focused osd control's id ("o-play", "o-scrub", ...), for the walk test
  [[nodiscard]] std::string focusedId() const;

  // what plays next ends the countdown at once (auto-continue) or asks, when the voiceover lacks the episode
  void startUpNext(std::int64_t nowMs);
  // the next episode is not in this voiceover: the card offers «Другая озвучка» instead of a countdown
  void setUpNextMissing(bool missing) { upNextMissing_ = missing; }
  // the osd shows (its rows over the film): what plays is on screen already
  [[nodiscard]] bool osdShown() const { return osd_.shown; }
  // the ui asks for the item after this one: the server plays it (media.play, or media.control's jump to its ref)
  std::function<void(const QueueItem& item, bool sameChoices)> playNext;
  // a voiceover, source or quality the switcher chose: the server plays it at the position kept (media.play)
  std::function<void(const SwitchChoice& choice, const std::string& choicesJson)> switched;
  // a panel or queue chip
  std::function<void(const std::string& panelId, Rml::Element* opener)> openPanel;
  std::function<void(Rml::Element* opener)> openQueue;

 private:
  std::string page() const;
  std::string rowB() const;
  std::string rowC() const;
  // the top-left badges and the line under the title: built again when the item's details fill them in
  std::string badges() const;
  std::string subLine() const;
  // the row's focusable controls, in order
  std::vector<Rml::Element*> items(OsdState::Row row) const;
  void place();
  void syncFromFocus();
  void seek(int direction, bool held);
  void refresh();
  void refreshSkip();
  // the skip pill's place: the osd's slot above the scrubber while the osd shows, a floating pill bottom-right
  // (above the compact bar) while it is hidden; never over a control
  void placeSkip();
  void refreshUpNext();
  void openTracks(cadence_v2_MediaTrackKind kind, Rml::Element* opener);
  void openQuality(Rml::Element* opener);
  void toggleAction(const ItemAction& action, Rml::Element* e);
  bool okHidden(const platform::KeyEvent& e);

  SideSheet& sheet_;
  OsdState osd_;
  std::int64_t bufferingSinceMs_ = -1;
  // the skip pill and the auto-skip's undo
  const Skip* skip_ = nullptr;
  std::int64_t skippedAtMs_ = -1;
  std::uint64_t skippedFromMs_ = 0;
  std::uint64_t skippedEndMs_ = 0;
  // up next: when it showed, cancelled for this item, and whether the voiceover has the next episode
  std::int64_t upNextAtMs_ = -1;
  bool upNextCancelled_ = false;
  bool upNextMissing_ = false;
  bool okDown_ = false;
  // focus on the skip pill in the osd's slot (↑ from the scrubber)
  bool slotFocused_ = false;
  std::string lastSubs_;
  // the codec line the badges show
  std::string shownCodecs_;
  std::int64_t lastRefresh_ = -1;
  bool firstLoad_ = true;
};

}  // namespace cadence::screen::routes

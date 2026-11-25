#pragma once

#include <optional>
#include <string>
#include <string_view>
#include <vector>

// the component library as rml (design r2 section 10): each builder returns markup in afterglow's classes, to be
// put into a document or an element's inner rml. text arguments are rml already: pass "[[key]]" for a catalog key
// (the translator rewrites it), and run anything third-party through esc()
namespace cadence::screen::ui::rml {

// xml-escaped, with "[[" and "{{" broken so neither the translator nor a data binding ever reads third-party text
std::string esc(std::string_view text);
// an art slot's image: "img:<id>" is the server's image proxy (the binder puts it on once decoded at the slot's
// size), anything else a texture source drawn at once
std::string artImage(std::string_view src, std::string_view cls);
// one icon of the bundled set by its tokens.json name (an unknown name draws "unknown")
std::string icon(std::string_view name, std::string_view classes = {});
// the codepoint's utf-8, or "" for an unknown name
std::string iconChar(std::string_view name);
[[nodiscard]] bool hasIcon(std::string_view name);

enum class CardKind { Poster, PosterJp, Wide, Square, Tile };

struct Card {
  CardKind kind = CardKind::Poster;
  std::string id;
  std::string title;
  std::string meta;
  // a second meta line (a title group's «tp · на 5 источниках»); empty for none
  std::string meta2;
  // the opaque item ref the server issued (data-ref: the screen context's focused and visible refs)
  std::string ref;
  // a texture source ("gen:art:seed", an image file, "img:<id>" from the server's image proxy); empty draws the
  // art's fill
  std::string art;
  // continue watching: 0-1 under the art; nullopt for none
  std::optional<double> progress;
  // badges' rml (badge()), under the art in the meta's place
  std::vector<std::string> badges;
  // what OK does (data-action), and whether long-press opens actions (data-actions)
  std::string action;
  std::string actions;
  bool unavailable = false;
};
std::string card(const Card& c);
// the row's last focusable: a card-sized tile with a chevron and «Все»
std::string allTile(CardKind kind, std::string_view action);
// a loading card of the same geometry; the empty state's ghosted outline
std::string skeletonCard(CardKind kind);
std::string ghostCard(CardKind kind);

enum class RowState { Ready, Loading, Slow, Empty, Error, Down, Offline };
struct Row {
  std::string id;
  std::string title;
  // the provider badge's rml, if any
  std::string badge;
  CardKind kind = CardKind::Poster;
  std::vector<Card> cards;
  RowState state = RowState::Ready;
  // section 9's words for a state (empty / error / down / slow)
  std::string stateText;
  // «Все →» at the end, with its action
  std::string allAction;
};
std::string row(const Row& r);

struct Button {
  std::string label;
  std::string icon;
  std::string action;
  bool primary = false;
  bool loading = false;
  bool refused = false;
  std::string id;
  std::string classes;
};
std::string button(const Button& b);
std::string iconButton(std::string_view icon, std::string_view word, std::string_view action, bool big = false);
std::string chip(std::string_view label, std::string_view icon = {}, std::optional<bool> on = std::nullopt,
                 std::string_view action = {});
std::string sourceChip(std::string_view label);

enum class BadgeKind { Provider, Live, Premiere, Quality, Subtitles, Explicit, New };
// provider: monogram + name ("tp", "torrplayer"); quality: "4K"; live: the dot + «ЭФИР» (text)
std::string badge(BadgeKind kind, std::string_view text, std::string_view monogram = {});

std::string progress(std::optional<double> fraction);
std::string spinner(std::string_view word = {});
std::string scrubber(double played, double buffered, std::string_view left, std::string_view right);

struct ListItem {
  std::string leadIcon;
  std::string leadArt;
  std::string title;
  std::string meta;
  std::string trail;
  std::string trailIcon;
  std::string action;
  std::string actions;
  bool focusable = true;
  std::string id;
  std::string ref;
  std::string classes;
};
std::string listItem(const ListItem& i);
// a settings row: label, current value, a chevron
std::string valueRow(std::string_view label, std::string_view value, std::string_view action);

struct Toggle {
  std::string label;
  std::string word;
  bool on = false;
  // empty: display-only (icon + word); else an actionable switch
  std::string action;
  bool pending = false;
  std::string id;
};
std::string toggle(const Toggle& t);

struct Slider {
  std::string label;
  double value = 0, min = 0, max = 100, step = 1;
  std::string valueLabel;
  std::string action;
  std::string id;
  // classes beside .slider (a card's timeline: "timeline")
  std::string cls;
  // false: shown alone, no focus ring (a timeline without an action)
  bool focusable = true;
};
std::string slider(const Slider& s);
// the label a slider shows for a value: valueLabel's number replaced, else the value and unit
std::string sliderLabel(double value, std::string_view unit);

std::string hero(std::string_view title, std::string_view meta, std::string_view art,
                 const std::vector<Button>& buttons);
// a failure, shown as it is (no giant icon): which source, the title, how it failed (codes, http status, time
// waited), the call on a quiet secondary line, then the actions («Повторить», «Другой источник», «Назад»). compact is
// the one-line form a row of many takes when only its feed failed. every text is rml (esc() what came from outside)
struct ErrorCard {
  std::string title;
  std::string source;
  // the reason in plain words and a hint at the next step (translator markers), above the code line
  std::string plain;
  std::string hint;
  // the code line: the failure as it came (codes, http status, time waited)
  std::string reason;
  std::string detail;
  std::vector<Button> actions;
  bool compact = false;
  std::string id;
};
std::string errorCard(const ErrorCard& e);
std::string stateBlock(std::string_view icon, std::string_view title, std::string_view body,
                       std::optional<Button> action);
// the clock and weather tile, bound to the "clock" data model (time, date, temp, weather)
std::string clockTile();

// the on-screen keyboard (r2): 7 x 5 alphabetical, layouts АБВ / ABC / 123, space, backspace, voice
enum class KeyLayout { Cyrillic, Latin, Digits };
std::string keyboard(KeyLayout layout, std::string_view entry);
// a layout's keys in order
std::vector<std::string> keyboardKeys(KeyLayout layout);

}  // namespace cadence::screen::ui::rml

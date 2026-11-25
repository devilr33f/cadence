#include "ui/core/rml.h"

#include "ui/tokens.h"

#include <algorithm>
#include <cmath>
#include <cstdio>

namespace cadence::screen::ui::rml {

namespace {

std::string utf8(char32_t c) {
  std::string out;
  if (c < 0x80) {
    out += static_cast<char>(c);
  } else if (c < 0x800) {
    out += static_cast<char>(0xC0 | (c >> 6));
    out += static_cast<char>(0x80 | (c & 0x3F));
  } else if (c < 0x10000) {
    out += static_cast<char>(0xE0 | (c >> 12));
    out += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
    out += static_cast<char>(0x80 | (c & 0x3F));
  } else {
    out += static_cast<char>(0xF0 | (c >> 18));
    out += static_cast<char>(0x80 | ((c >> 12) & 0x3F));
    out += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
    out += static_cast<char>(0x80 | (c & 0x3F));
  }
  return out;
}

const char* kindClass(CardKind k) {
  switch (k) {
    case CardKind::Poster:
      return "poster";
    case CardKind::PosterJp:
      return "poster-jp";
    case CardKind::Wide:
      return "wide";
    case CardKind::Square:
      return "square";
    case CardKind::Tile:
      return "tile";
  }
  return "poster";
}

std::string attr(std::string_view name, std::string_view value) {
  if (value.empty()) return {};
  return " " + std::string(name) + "=\"" + esc(value) + "\"";
}

std::string focusDecor() { return "<div class=\"glow\"/><div class=\"ring\"/><div class=\"hold\"/>"; }

std::string fmt(double v) {
  char buf[32];
  std::snprintf(buf, sizeof buf, "%.4f", v);
  return buf;
}

}  // namespace

std::string artImage(std::string_view src, std::string_view cls) {
  if (src.starts_with("img:")) {
    return "<div class=\"" + std::string(cls) + " netimg\" data-img=\"" + esc(src.substr(4)) + "\"/>";
  }
  return "<div class=\"" + std::string(cls) + "\" style=\"decorator: image(&quot;" + esc(src) + "&quot; cover);\"/>";
}

std::string esc(std::string_view text) {
  std::string out;
  out.reserve(text.size());
  for (std::size_t i = 0; i < text.size(); ++i) {
    const char c = text[i];
    switch (c) {
      case '&':
        out += "&amp;";
        break;
      case '<':
        out += "&lt;";
        break;
      case '>':
        out += "&gt;";
        break;
      case '"':
        out += "&quot;";
        break;
      case '[':
      case '{':
        out += c;
        // a zero-width space between two brackets keeps them from opening a key or a binding
        if (i + 1 < text.size() && text[i + 1] == c) out += "\xE2\x80\x8B";
        break;
      default:
        out += c;
    }
  }
  return out;
}

bool hasIcon(std::string_view name) {
  for (const auto& i : tokens::kIcons) {
    if (i.name == name) return true;
  }
  return false;
}

std::string iconChar(std::string_view name) {
  for (const auto& i : tokens::kIcons) {
    if (i.name == name) return utf8(i.codepoint);
  }
  return {};
}

std::string icon(std::string_view name, std::string_view classes) {
  auto c = iconChar(name);
  if (c.empty()) c = iconChar("unknown");
  return "<span class=\"icon" + (classes.empty() ? std::string() : " " + std::string(classes)) + "\">" + c + "</span>";
}

std::string card(const Card& c) {
  std::string cls = std::string("card f ") + kindClass(c.kind);
  if (c.unavailable) cls += " unavailable";
  if (!c.ref.empty()) cls += " has-ref";
  std::string s = "<div class=\"" + cls + "\"" + attr("id", c.id) + attr("data-ref", c.ref) + attr("data-action", c.action) +
                  attr("data-actions", c.actions) + attr("data-art", c.art) + ">";
  s += "<div class=\"art\">";
  // the art is clipped by the card's radius (overflow on a rounded box), never by an image mask
  if (!c.art.empty()) {
    // a network image comes rounded from its decode (images/binder.cpp): its clip needs no mask; a stand-in's still
    // takes the clip's rounded overflow
    s += std::string(c.art.starts_with("img:") ? "<div class=\"clip pre\">" : "<div class=\"clip\">") + artImage(c.art, "img") + "</div>";
  }
  s += "<div class=\"hair\"/>";
  s += focusDecor() + "</div>";
  if (c.progress) {
    s += "<div class=\"progress\"><div class=\"fill\" style=\"transform: scaleX(" + fmt(std::clamp(*c.progress, 0.0, 1.0)) +
         ");\"/></div>";
  }
  s += "<div class=\"title\">" + c.title + "</div>";
  if (!c.badges.empty()) {
    s += "<div class=\"badges\">";
    for (const auto& b : c.badges) s += b;
    s += "</div>";
  } else if (!c.meta.empty()) {
    // no empty line: the provider line («tp · anime365») sits right under the title when there is no meta
    s += "<div class=\"meta\">" + c.meta + "</div>";
  }
  if (!c.meta2.empty()) s += "<div class=\"meta\">" + c.meta2 + "</div>";
  if (c.unavailable) s += "<div class=\"meta\">[[common.unavailable_now]]</div>";
  return s + "</div>";
}

std::string allTile(CardKind kind, std::string_view action) {
  return "<div class=\"card f all " + std::string(kindClass(kind)) + "\"" + attr("data-action", action) +
         "><div class=\"art\">" + icon("chevron_right") + focusDecor() + "</div><div class=\"title\">[[common.all]]</div></div>";
}

std::string skeletonCard(CardKind kind) {
  return "<div class=\"card " + std::string(kindClass(kind)) +
         "\"><div class=\"art skel\"/><div class=\"skel line\" style=\"width: 70%;\"/><div class=\"skel line\" "
         "style=\"width: 45%;\"/></div>";
}

std::string ghostCard(CardKind kind) {
  return "<div class=\"card ghost " + std::string(kindClass(kind)) + "\"><div class=\"art\"/></div>";
}

std::string row(const Row& r) {
  std::string s = "<div class=\"row\"" + attr("id", r.id) + "><div class=\"row-header\"><div class=\"name\">" + r.title +
                  "</div>" + r.badge + "</div><div class=\"track\">";
  switch (r.state) {
    case RowState::Ready:
    case RowState::Offline:
      for (const auto& c : r.cards) s += card(c);
      if (!r.allAction.empty()) s += allTile(r.kind, r.allAction);
      break;
    case RowState::Loading:
    case RowState::Slow:
      for (int i = 0; i < 7; ++i) s += skeletonCard(r.kind);
      break;
    case RowState::Empty:
      s += ghostCard(r.kind);
      // the empty text is the row's focusable, so focus has somewhere to stay
      s += "<div class=\"row-state f\"><div class=\"glow\"/><div class=\"ring\"/>" + icon("info") + "<span class=\"title\">" +
           (r.stateText.empty() ? std::string("[[ui.empty]]") : r.stateText) + "</span></div>";
      break;
    case RowState::Error:
      s += "<div class=\"row-state\">" + icon("triangle") + "<span>[[common.load_failed]]</span></div>";
      s += button(Button{"[[common.retry]]", "refresh", "retry:" + r.id, false, false, false, {}, "default-focus"});
      break;
    case RowState::Down:
      s += "<div class=\"row-state\">" + r.badge + icon("offline") + "<span>" + r.stateText + "</span></div>";
      s += button(Button{"[[common.retry]]", "refresh", "retry:" + r.id, false, false, false, {}, {}});
      break;
  }
  s += "</div>";
  if (r.state == RowState::Slow) s += "<div class=\"row-state\">[[ui.slow]]</div>";
  s += "<div class=\"cline\"/></div>";
  if (r.state == RowState::Offline) s = "<div class=\"offline-dim\">" + s + "</div>";
  return s;
}

std::string button(const Button& b) {
  std::string cls = "btn f";
  if (b.primary) cls += " primary";
  if (b.loading) cls += " loading";
  if (b.refused) cls += " refused";
  if (!b.classes.empty()) cls += " " + b.classes;
  std::string s = "<div class=\"" + cls + "\"" + attr("id", b.id) + attr("data-action", b.action) + ">" + focusDecor();
  if (b.refused) {
    s += icon("locked");
  } else if (!b.icon.empty()) {
    s += icon(b.icon);
  }
  // always there (hidden until .loading): a route may set the class later (a retry under way) with no redraw
  s += "<div class=\"spinner\"/>";
  return s + "<span>" + b.label + "</span></div>";
}

std::string iconButton(std::string_view iconName, std::string_view word, std::string_view action, bool big) {
  return "<div class=\"ibtn f" + std::string(big ? " big" : "") + "\"" + attr("data-action", action) + ">" +
         focusDecor() + icon(iconName) + "<div class=\"word\">" + std::string(word) + "</div></div>";
}

std::string chip(std::string_view label, std::string_view iconName, std::optional<bool> on, std::string_view action) {
  std::string cls = "chip";
  if (!action.empty() || on) cls += " f";
  if (on && *on) cls += " on";
  std::string s = "<div class=\"" + cls + "\"" + attr("data-action", action) + ">";
  if (cls.find(" f") != std::string::npos) s += focusDecor();
  if (on) s += icon("check", "check");
  if (!iconName.empty()) s += icon(iconName);
  return s + "<span>" + std::string(label) + "</span></div>";
}

std::string sourceChip(std::string_view label) {
  return "<div class=\"chip source\"><span>" + std::string(label) + "</span></div>";
}

std::string badge(BadgeKind kind, std::string_view text, std::string_view monogram) {
  switch (kind) {
    case BadgeKind::Provider:
      return "<div class=\"badge\"><span class=\"mono2\">" + std::string(monogram) + "</span><span>" +
             std::string(text) + "</span></div>";
    case BadgeKind::Live:
      return "<div class=\"badge\"><div class=\"dot\"/><span>" +
             (text.empty() ? std::string("[[audio.live]]") : std::string(text)) + "</span></div>";
    case BadgeKind::Premiere:
      return "<div class=\"badge code\"><span>" + (text.empty() ? std::string("[[ui.premiere]]") : std::string(text)) +
             "</span></div>";
    case BadgeKind::Quality:
      return "<div class=\"badge code\"><span>" + std::string(text) + "</span></div>";
    case BadgeKind::Subtitles:
      return "<div class=\"badge code\">" + icon("subtitles") + "</div>";
    case BadgeKind::Explicit:
      return "<div class=\"badge explicit\">E</div>";
    case BadgeKind::New:
      return "<div class=\"badge code\"><span>" +
             (text.empty() ? std::string("[[title.badge.new]]") : std::string(text)) + "</span></div>";
  }
  return {};
}

std::string progress(std::optional<double> fraction) {
  if (!fraction) return "<div class=\"progress indeterminate\"><div class=\"fill\"/></div>";
  return "<div class=\"progress\"><div class=\"fill\" style=\"transform: scaleX(" + fmt(std::clamp(*fraction, 0.0, 1.0)) +
         ");\"/></div>";
}

std::string spinner(std::string_view word) {
  // a spinner never sits alone: the word comes in after 2 s (the ui sets .late on .spinbox)
  std::string s = "<div class=\"hstack spinbox\" style=\"gap: 16dp;\"><div class=\"spinner\"/>";
  if (!word.empty()) s += "<span class=\"spin-word\">" + std::string(word) + "</span>";
  return s + "</div>";
}

std::string scrubber(double played, double buffered, std::string_view left, std::string_view right) {
  return "<div class=\"scrubber\"><div class=\"track\"><div class=\"buffered\" style=\"width: " +
         fmt(std::clamp(buffered, 0.0, 1.0) * 100) + "%;\"/><div class=\"played\" style=\"width: " +
         fmt(std::clamp(played, 0.0, 1.0) * 100) + "%;\"/></div><div class=\"times\"><span>" + std::string(left) +
         "</span><span>" + std::string(right) + "</span></div></div>";
}

std::string listItem(const ListItem& i) {
  std::string s = "<div class=\"item" + std::string(i.focusable ? " f" : "") + (i.ref.empty() ? "" : " has-ref") +
                  (i.classes.empty() ? "" : " " + i.classes) + "\"" + attr("id", i.id) + attr("data-ref", i.ref) +
                  attr("data-action", i.action) + attr("data-actions", i.actions) + ">";
  if (i.focusable) s += focusDecor();
  if (!i.leadArt.empty()) {
    s += artImage(i.leadArt, "lead");
  } else if (!i.leadIcon.empty()) {
    s += icon(i.leadIcon, "lead");
  }
  s += "<div class=\"texts\"><div class=\"ititle\">" + i.title + "</div>";
  if (!i.meta.empty()) s += "<div class=\"imeta\">" + i.meta + "</div>";
  s += "</div>";
  if (!i.trail.empty()) s += "<div class=\"trail\">" + i.trail + "</div>";
  if (!i.trailIcon.empty()) s += icon(i.trailIcon, "trail");
  return s + "</div>";
}

std::string valueRow(std::string_view label, std::string_view value, std::string_view action) {
  ListItem i;
  i.title = label;
  i.trail = value;
  i.trailIcon = "chevron_right";
  i.action = action;
  return listItem(i);
}

std::string toggle(const Toggle& t) {
  const std::string word = !t.word.empty() ? t.word : (t.on ? "[[ui.on]]" : "[[ui.off]]");
  if (t.action.empty()) {
    return "<div class=\"toggle" + std::string(t.on ? " on" : "") + "\"" + attr("id", t.id) + ">" +
           icon(t.on ? "on" : "off", "state") + "<span>" + t.label + " · " + word + "</span></div>";
  }
  std::string cls = "toggle f";
  if (t.on) cls += " on";
  if (t.pending) cls += " pending";
  return "<div class=\"" + cls + "\"" + attr("id", t.id) + attr("data-action", t.action) + " data-kind=\"toggle\">" +
         focusDecor() + "<span class=\"tlabel\">" + t.label + "</span><div class=\"switch\"><div class=\"knob\"/></div>" +
         "<span class=\"tword\">" + word + "</span><div class=\"pending-line\"/></div>";
}

std::string sliderLabel(double value, std::string_view unit) {
  char buf[48];
  if (std::fabs(value - std::round(value)) < 1e-9) {
    std::snprintf(buf, sizeof buf, "%.0f", value);
  } else {
    std::snprintf(buf, sizeof buf, "%.1f", value);
  }
  std::string s = buf;
  if (!unit.empty()) s += (unit == "%" ? " " : " ") + std::string(unit);
  return s;
}

std::string slider(const Slider& s) {
  const double span = s.max > s.min ? s.max - s.min : 1;
  const double f = std::clamp((s.value - s.min) / span, 0.0, 1.0);
  char data[200];
  std::snprintf(data, sizeof data, " data-kind=\"slider\" data-value=\"%g\" data-min=\"%g\" data-max=\"%g\" data-step=\"%g\"",
                s.value, s.min, s.max, s.step > 0 ? s.step : 1);
  const std::string cls = std::string("slider") + (s.focusable ? " f" : "") + (s.cls.empty() ? "" : " " + s.cls);
  return "<div class=\"" + cls + "\"" + attr("id", s.id) + attr("data-action", s.action) + data + ">" +
         (s.focusable ? focusDecor() : std::string()) +
         "<div class=\"shead\"><span>" + s.label + "</span><span class=\"svalue\">" + s.valueLabel +
         "</span></div><div class=\"strack\"><div class=\"sfill\" style=\"transform: scaleX(" + fmt(f) +
         ");\"/><div class=\"thumb\" style=\"left: " + fmt(f * 100) + "%;\"/></div></div>";
}

std::string hero(std::string_view title, std::string_view meta, std::string_view art,
                 const std::vector<Button>& buttons) {
  // no art: the page draws the backdrop under it (home's, which cross-fades as focus moves)
  const std::string back = art.empty() ? std::string() : artImage(art, "backdrop") + "<div class=\"shade\"/>";
  std::string s = "<div class=\"hero\">" + back + "<div class=\"body\"><div class=\"htitle\">" +
                  std::string(title) + "</div><div class=\"hmeta\">" + std::string(meta) +
                  "</div><div class=\"hstack\" style=\"gap: 24dp;\">";
  for (const auto& b : buttons) s += button(b);
  return s + "</div></div></div>";
}

std::string errorCard(const ErrorCard& e) {
  std::string s = "<div class=\"errcard" + std::string(e.compact ? " einline" : "") + "\"" + attr("id", e.id) + ">";
  s += "<div class=\"etext\">";
  if (!e.source.empty() || e.compact) {
    s += "<div class=\"ehead\"><span class=\"edot\"/><span class=\"esource\">" + e.source + "</span>";
    // one line: the plain reason when there is one, else the code
    if (e.compact) s += "<span class=\"eplain\">" + (!e.plain.empty() ? e.plain : e.reason) + "</span>";
    s += "</div>";
  }
  if (!e.compact) {
    s += "<div class=\"etitle\">" + e.title + "</div>";
    if (!e.plain.empty()) s += "<div class=\"eplain\">" + e.plain + "</div>";
    if (!e.hint.empty()) s += "<div class=\"ehint\">" + e.hint + "</div>";
    if (!e.reason.empty()) s += "<div class=\"ereason\">" + e.reason + "</div>";
    if (!e.detail.empty()) s += "<div class=\"edetail\">" + e.detail + "</div>";
  } else {
    // the hint, then the code and the call on the quiet line
    if (!e.hint.empty()) s += "<div class=\"ehint\">" + e.hint + "</div>";
    std::string quiet = !e.plain.empty() ? e.reason : std::string();
    if (!e.detail.empty()) quiet += (quiet.empty() ? "" : " · ") + e.detail;
    if (!quiet.empty()) s += "<div class=\"edetail\">" + quiet + "</div>";
  }
  s += "</div><div class=\"eactions\">";
  for (const auto& b : e.actions) s += button(b);
  return s + "</div></div>";
}

std::string stateBlock(std::string_view iconName, std::string_view title, std::string_view body,
                       std::optional<Button> action) {
  std::string s = "<div class=\"state-block\">" + icon(iconName) + "<div class=\"stitle\">" + std::string(title) +
                  "</div><div class=\"sbody\">" + std::string(body) + "</div>";
  if (action) s += button(*action);
  return s + "</div>";
}

std::string clockTile() {
  return "<div class=\"clock\" data-model=\"clock\"><div class=\"time\">{{time}}</div><div class=\"date\">{{date}}</div>"
         "<div class=\"weather\"><span class=\"icon\">{{weatherIcon}}</span> {{temp}}</div></div>";
}

std::vector<std::string> keyboardKeys(KeyLayout layout) {
  switch (layout) {
    case KeyLayout::Cyrillic:
      return {"а", "б", "в", "г", "д", "е", "ё", "ж", "з", "и", "й", "к", "л", "м", "н", "о", "п", "р",
              "с", "т", "у", "ф", "х", "ц", "ч", "ш", "щ", "ъ", "ы", "ь", "э", "ю", "я", "-", ","};
    case KeyLayout::Latin:
      return {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r",
              "s", "t", "u", "v", "w", "x", "y", "z", "'", "-", ",", ".", "!", "?", "&", "@", "#"};
    case KeyLayout::Digits:
      return {"1", "2", "3", "4", "5", "6", "7", "8", "9", "0", "-", "+", "=", "/", ":", ";", "(", ")",
              "%", "№", "°", "\"", "*", "_", "$", "€", "₽", ".", ",", "!", "?", "'", "#", "@", "&"};
  }
  return {};
}

std::string keyboard(KeyLayout layout, std::string_view entry) {
  std::string s = "<div class=\"osk\"><div class=\"entry\">" + esc(entry) + "<span class=\"caret\">|</span></div>";
  s += "<div class=\"keys\">";
  for (const auto& k : keyboardKeys(layout)) {
    // ё on a long press of е (it is also a key of its own in the alphabetical grid)
    const bool e = layout == KeyLayout::Cyrillic && k == "е";
    s += "<div class=\"key f\" data-action=\"key:" + esc(k) + "\"" + (e ? " data-actions=\"key:ё\"" : "") + ">" + esc(k) +
         "<div class=\"hold\"/></div>";
  }
  const auto mode = [&](KeyLayout l, const char* label, const char* id) {
    return "<div class=\"key mode f" + std::string(layout == l ? " on" : "") + "\" data-action=\"layout:" + id + "\">" +
           label + "</div>";
  };
  s += mode(KeyLayout::Cyrillic, "АБВ", "ru") + mode(KeyLayout::Latin, "ABC", "en") + mode(KeyLayout::Digits, "123", "digits");
  s += "<div class=\"key wide2 f\" data-action=\"key:space\">" + icon("space") + "</div>";
  s += "<div class=\"key f\" data-action=\"key:backspace\">" + icon("backspace") + "</div>";
  s += "<div class=\"key f\" data-action=\"key:voice\">" + icon("mic") + "</div>";
  return s + "</div></div>";
}

}  // namespace cadence::screen::ui::rml

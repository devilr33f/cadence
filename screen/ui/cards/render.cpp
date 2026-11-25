#include "ui/cards/render.h"

#include "ui/cards/timeline.h"

#include "ui/core/rml.h"
#include "ui/rml/sprites.h"

#include <algorithm>
#include <cctype>
#include <cmath>
#include <cstdio>

namespace cadence::screen::ui::cards {

namespace {

using rml::esc;

std::string e(const char* s) { return esc(str(s)); }

std::string a(std::string_view name, std::string_view value) {
  if (value.empty()) return {};
  return " " + std::string(name) + "=\"" + esc(value) + "\"";
}

const char* toneName(const cadence_cards_v1_Tone* t) {
  switch (val(t)) {
    case cadence_cards_v1_Tone_TONE_ACCENT:
      return "accent";
    case cadence_cards_v1_Tone_TONE_SUCCESS:
      return "success";
    case cadence_cards_v1_Tone_TONE_WARNING:
      return "warning";
    case cadence_cards_v1_Tone_TONE_DANGER:
      return "danger";
    case cadence_cards_v1_Tone_TONE_INFO:
      return "info";
    default:
      return "neutral";
  }
}

const char* gapClass(const cadence_cards_v1_Gap* g) {
  switch (val(g)) {
    case cadence_cards_v1_Gap_GAP_S:
      return "gap-s2";
    case cadence_cards_v1_Gap_GAP_L:
      return "gap-s6";
    default:
      return "gap-s4";
  }
}

// a server's image ref: "img:<id>" is the image proxy's, fetched, decoded off the ui thread and fitted to its box by
// the image binder; anything else draws the ref's stand-in art
std::string art(const char* ref) {
  const auto r = str(ref);
  if (r.starts_with("img:")) return std::string(r);
  return "gen:art:" + std::string(r);
}

// an image of a ref in a box of class cls: a network image's slot, or the stand-in as its decorator
std::string artBox(const char* tag, const char* ref, std::string_view cls, std::string_view inner = {}) {
  const auto src = art(ref);
  if (src.starts_with("img:")) {
    return "<" + std::string(tag) + " class=\"" + std::string(cls) + " netimg\" data-img=\"" + esc(src.substr(4)) + "\">" +
           std::string(inner) + "</" + tag + ">";
  }
  return "<" + std::string(tag) + " class=\"" + std::string(cls) + "\" style=\"decorator: image(&quot;" + esc(src) +
         "&quot; cover);\">" + std::string(inner) + "</" + tag + ">";
}

std::string actionAttrs(const cadence_cards_v1_Action* action) {
  if (action == nullptr) return {};
  return a("data-action", str(action->id)) + a("data-confirm", str(action->confirm));
}

std::string stateIcon(cadence_cards_v1_StateKind k) {
  switch (k) {
    case cadence_cards_v1_StateKind_STATE_KIND_ON:
      return "on";
    case cadence_cards_v1_StateKind_STATE_KIND_OFF:
      return "off";
    case cadence_cards_v1_StateKind_STATE_KIND_OPEN:
      return "open";
    case cadence_cards_v1_StateKind_STATE_KIND_CLOSED:
      return "closed";
    case cadence_cards_v1_StateKind_STATE_KIND_LOCKED:
      return "locked";
    case cadence_cards_v1_StateKind_STATE_KIND_UNLOCKED:
      return "unlocked";
    case cadence_cards_v1_StateKind_STATE_KIND_UNAVAILABLE:
      return "unavailable";
    default:
      return "unknown";
  }
}

// a state's tone when the server names none: on is success, open or unlocked a warning, the rest neutral
std::string stateTone(cadence_cards_v1_StateKind k) {
  switch (k) {
    case cadence_cards_v1_StateKind_STATE_KIND_ON:
      return "success";
    case cadence_cards_v1_StateKind_STATE_KIND_OPEN:
    case cadence_cards_v1_StateKind_STATE_KIND_UNLOCKED:
      return "warning";
    case cadence_cards_v1_StateKind_STATE_KIND_UNAVAILABLE:
      return "danger";
    default:
      return "neutral";
  }
}

// a results card (a list of posts or links) is held to a few compact rows in the drawer
std::string kindClass(const cadence_cards_v1_CardBlock& root) {
  for (pb_size_t i = 0; i < root.children_count; ++i) {
    const auto& n = root.children[i];
    if (n.which_kind != cadence_cards_v1_Node_paged_list_tag || n.kind.paged_list->items_count == 0) continue;
    switch (n.kind.paged_list->items[0].which_kind) {
      case cadence_cards_v1_Node_post_tag:
        return " k-posts";
      case cadence_cards_v1_Node_link_tag:
        return " k-links";
      case cadence_cards_v1_Node_media_tile_tag:
        return " k-tiles";
      default:
        return " k-list";
    }
  }
  return {};
}

std::string badgeOf(const cadence_cards_v1_Badge& b) {
  const auto t = str(b.text);
  switch (val(b.kind)) {
    case cadence_cards_v1_BadgeKind_BADGE_KIND_LIVE:
      return rml::badge(rml::BadgeKind::Live, esc(t));
    case cadence_cards_v1_BadgeKind_BADGE_KIND_PREMIERE:
      return rml::badge(rml::BadgeKind::Premiere, esc(t));
    case cadence_cards_v1_BadgeKind_BADGE_KIND_HD:
      return rml::badge(rml::BadgeKind::Quality, "HD");
    case cadence_cards_v1_BadgeKind_BADGE_KIND_4K:
      return rml::badge(rml::BadgeKind::Quality, "4K");
    case cadence_cards_v1_BadgeKind_BADGE_KIND_HDR:
      return rml::badge(rml::BadgeKind::Quality, "HDR");
    case cadence_cards_v1_BadgeKind_BADGE_KIND_CC:
      return rml::badge(rml::BadgeKind::Subtitles, {});
    case cadence_cards_v1_BadgeKind_BADGE_KIND_EXPLICIT:
      return rml::badge(rml::BadgeKind::Explicit, {});
    case cadence_cards_v1_BadgeKind_BADGE_KIND_NEW:
      return rml::badge(rml::BadgeKind::New, esc(t));
    default: {
      // a provider: its two-letter monogram and plain name, no logo
      std::string mono;
      for (char c : t) {
        if (mono.size() >= 2) break;
        if (static_cast<unsigned char>(c) < 0x80 && std::isalpha(static_cast<unsigned char>(c))) mono += c;
      }
      return rml::badge(rml::BadgeKind::Provider, esc(t), esc(mono));
    }
  }
}

std::string fmt(double v) {
  char buf[32];
  std::snprintf(buf, sizeof buf, "%.4f", v);
  return buf;
}

struct Renderer {
  RenderOptions opt;

  std::string nodes(const cadence_cards_v1_Node* kids, pb_size_t count) {
    std::string s;
    for (pb_size_t i = 0; i < count; ++i) s += node(kids[i]);
    return s;
  }

  std::string post(const cadence_cards_v1_Post& p, bool quoted) {
    std::string cls = "cpost";
    if (p.action != nullptr && !quoted) cls += " f";
    if (quoted) cls += " cquote";
    std::string s = "<div class=\"" + cls + "\"" + (quoted ? std::string() : actionAttrs(p.action)) + ">";
    if (p.action != nullptr && !quoted) s += "<div class=\"glow\"/><div class=\"ring\"/>";
    if (p.reply_to != nullptr) {
      s += "<div class=\"cmeta\">[[cards.reply_to|handle=" + esc(str(p.reply_to)) + "]]</div>";
    }
    if (p.reposted_by != nullptr) {
      s += "<div class=\"cmeta\">[[cards.reposted|handle=" + esc(str(p.reposted_by)) + "]]</div>";
    }
    s += "<div class=\"phead\">";
    if (p.avatar != nullptr) s += artBox("div", p.avatar->ref, "avatar");
    s += "<div class=\"pwho\"><span class=\"pauthor\">" + e(p.author) + "</span>";
    if (p.handle != nullptr) s += " <span class=\"phandle\">" + e(p.handle) + "</span>";
    if (p.time != nullptr) s += " <span class=\"phandle\">· " + e(p.time) + "</span>";
    s += "</div>";
    if (p.source != nullptr) s += "<span class=\"psource\">" + e(p.source) + "</span>";
    s += "</div><div class=\"ptext\">" + e(p.text) + "</div>";
    if (p.media != nullptr) {
      s += artBox("div", p.media->ref, "pmedia");
    }
    if (p.note != nullptr) {
      s += "<div class=\"cnote\"><div class=\"cstripe stripe-info\"/><div class=\"nlabel tone-info\">" + rml::icon("info") + " " +
           (p.note->label != nullptr ? e(p.note->label) : std::string()) + "</div><div class=\"ntext\">" + e(p.note->text) +
           "</div></div>";
    }
    if (p.quote != nullptr) s += post(*p.quote, true);
    if (p.stats_count > 0) {
      s += "<div class=\"pstats\">";
      for (pb_size_t i = 0; i < p.stats_count; ++i) {
        const auto& st = p.stats[i];
        const char* ic = "view";
        switch (val(st.kind)) {
          case cadence_cards_v1_StatKind_STAT_KIND_REPLY:
            ic = "reply";
            break;
          case cadence_cards_v1_StatKind_STAT_KIND_REPOST:
            ic = "repost";
            break;
          case cadence_cards_v1_StatKind_STAT_KIND_LIKE:
            ic = "heart";
            break;
          default:
            break;
        }
        s += "<span class=\"pstat\">" + rml::icon(ic) + " " + e(st.label) + "</span>";
      }
      s += "</div>";
    }
    return s + "</div>";
  }

  std::string node(const cadence_cards_v1_Node& n) {
    switch (n.which_kind) {
      case cadence_cards_v1_Node_row_tag:
        return "<div class=\"crow " + std::string(gapClass(n.kind.row->gap)) + "\">" +
               nodes(n.kind.row->children, n.kind.row->children_count) + "</div>";
      case cadence_cards_v1_Node_column_tag:
        return "<div class=\"ccol " + std::string(gapClass(n.kind.column->gap)) + "\">" +
               nodes(n.kind.column->children, n.kind.column->children_count) + "</div>";
      case cadence_cards_v1_Node_grid_tag: {
        const auto cols = std::clamp<std::uint32_t>(val(n.kind.grid->columns), 2, 4);
        std::string s = "<div class=\"cgrid c" + std::to_string(cols) + " " + gapClass(n.kind.grid->gap) + "\">";
        for (pb_size_t i = 0; i < n.kind.grid->children_count; ++i) {
          s += "<div class=\"cell\">" + node(n.kind.grid->children[i]) + "</div>";
        }
        return s + "</div>";
      }
      case cadence_cards_v1_Node_text_tag: {
        const auto& t = *n.kind.text;
        const char* style = "t-body";
        switch (val(t.style)) {
          case cadence_cards_v1_TextStyle_TEXT_STYLE_TITLE:
            style = "t-title-s";
            break;
          case cadence_cards_v1_TextStyle_TEXT_STYLE_META:
            style = "t-body cmeta";
            break;
          case cadence_cards_v1_TextStyle_TEXT_STYLE_FIGURES:
            style = "t-figures";
            break;
          default:
            break;
        }
        const std::string tone = t.tone != nullptr ? std::string(" tone-") + toneName(t.tone) : std::string();
        if (val(t.style) == cadence_cards_v1_TextStyle_TEXT_STYLE_TITLE) {
          // a title the layout holds to its lines (a track's beside its cover) ends in «…» where it was cut: rmlui
          // has no line clamp, so the host fits its words once laid out
          return "<div class=\"ctext clamp " + std::string(style) + tone + "\"" + a("data-text", str(t.text)) + ">" + e(t.text) +
                 "</div>";
        }
        return "<div class=\"ctext " + std::string(style) + tone + "\">" + e(t.text) + "</div>";
      }
      case cadence_cards_v1_Node_long_text_tag: {
        const auto& l = *n.kind.long_text;
        const auto lines = val(l.collapsed_lines) != 0 ? val(l.collapsed_lines) : 4u;
        const bool reading = val(l.reading);
        const int lineDp = reading ? 44 : 36;
        std::string s = "<div class=\"clong f" + std::string(reading ? " reading" : "") + (val(l.tail) ? " tail" : "") +
                        "\" data-kind=\"expand\" data-collapsed=\"" + std::to_string(lines * static_cast<unsigned>(lineDp)) +
                        "dp\" style=\"max-height: " + std::to_string(lines * static_cast<unsigned>(lineDp)) + "dp;\"><div class=\"glow\"/><div class=\"ring\"/><div class=\"ltext\">" + e(l.text) + "</div></div>";
        if (val(l.tail)) s += "<div class=\"newbelow hidden\">[[cards.new_below]]</div>";
        return s;
      }
      case cadence_cards_v1_Node_icon_tag: {
        const auto size = val(n.kind.icon->size) == 48 ? "cicon48" : "cicon32";
        const std::string tone = n.kind.icon->tone != nullptr ? std::string(" tone-") + toneName(n.kind.icon->tone) : "";
        const auto name = str(n.kind.icon->name);
        return rml::icon(rml::hasIcon(name) ? name : "unknown", std::string(size) + tone);
      }
      case cadence_cards_v1_Node_image_tag: {
        const char* cls = "img-square";
        switch (val(n.kind.image->aspect)) {
          case cadence_cards_v1_Aspect_ASPECT_POSTER:
            cls = "img-poster";
            break;
          case cadence_cards_v1_Aspect_ASPECT_WIDE:
            cls = "img-wide";
            break;
          case cadence_cards_v1_Aspect_ASPECT_AVATAR:
            cls = "img-avatar";
            break;
          default:
            break;
        }
        return artBox("div", n.kind.image->ref, "cimage " + std::string(cls), "<div class=\"hair\"/>");
      }
      case cadence_cards_v1_Node_state_tag: {
        // a pill: a dot in the state's tone and its word (never colour alone); the card's icon names the domain
        const auto& st = *n.kind.state;
        const std::string tone = st.tone != nullptr ? toneName(st.tone) : stateTone(val(st.kind));
        return "<div class=\"cstate s-" + stateIcon(val(st.kind)) + "\"><div class=\"sdot stripe-" + tone + "\"/><span>" +
               e(st.label) + "</span></div>";
      }
      case cadence_cards_v1_Node_value_tag: {
        const auto& v = *n.kind.value;
        std::string s = "<div class=\"cvalue\">";
        if (v.label != nullptr) s += "<div class=\"vlabel\">" + e(v.label) + "</div>";
        s += "<div class=\"vline\"><span class=\"vnum\">" + e(v.value) + "</span>";
        if (v.unit != nullptr) s += "<span class=\"vunit\">" + e(v.unit) + "</span>";
        switch (val(v.trend)) {
          case cadence_cards_v1_Trend_TREND_UP:
            s += rml::icon("trend_up", "vtrend");
            break;
          case cadence_cards_v1_Trend_TREND_DOWN:
            s += rml::icon("trend_down", "vtrend");
            break;
          case cadence_cards_v1_Trend_TREND_FLAT:
            s += rml::icon("trend_flat", "vtrend");
            break;
          default:
            break;
        }
        return s + "</div></div>";
      }
      case cadence_cards_v1_Node_meter_tag: {
        const auto& m = *n.kind.meter;
        const double span = val(m.max) - val(m.min);
        const double f = span > 0 ? std::clamp((val(m.value) - val(m.min)) / span, 0.0, 1.0) : 0;
        return "<div class=\"cmeter\"><div class=\"shead\"><span>" + e(m.label) + "</span><span class=\"svalue\">" +
               e(m.value_label) + "</span></div><div class=\"mtrack\"><div class=\"mfill\" style=\"transform: scaleX(" + fmt(f) +
               ");\"/></div></div>";
      }
      case cadence_cards_v1_Node_progress_tag: {
        const auto& p = *n.kind.progress;
        std::string s = "<div class=\"cprogress\">";
        if (p.label != nullptr) s += "<div class=\"cmeta\">" + e(p.label) + "</div>";
        const std::optional<double> f = p.which_amount == cadence_cards_v1_Progress_fraction_tag
                                            ? std::optional<double>(val(p.amount.fraction))
                                            : std::nullopt;
        return s + rml::progress(f) + "</div>";
      }
      case cadence_cards_v1_Node_countdown_tag: {
        const auto& c = *n.kind.countdown;
        const auto format = val(c.format);
        std::string s = "<div class=\"ccountdown\"" + a("data-ends-at", std::to_string(val(c.ends_at_ms))) +
                        " data-format=\"" + std::to_string(static_cast<int>(format)) + "\">";
        if (c.label != nullptr) s += "<div class=\"cmeta\">" + e(c.label) + "</div>";
        return s + "<div class=\"cdtime\">" + countdownText(val(c.ends_at_ms), opt.nowEpochMs, format) + "</div></div>";
      }
      case cadence_cards_v1_Node_steps_tag: {
        std::string s = "<div class=\"csteps\">";
        for (pb_size_t i = 0; i < n.kind.steps->items_count; ++i) {
          const auto& st = n.kind.steps->items[i];
          std::string mark;
          std::string cls = "cstep";
          switch (val(st.status)) {
            case cadence_cards_v1_StepStatus_STEP_STATUS_RUNNING:
              mark = "<div class=\"spinner small\"/>";
              cls += " running";
              break;
            case cadence_cards_v1_StepStatus_STEP_STATUS_DONE:
              mark = rml::icon("check", "tone-success");
              cls += " done";
              break;
            case cadence_cards_v1_StepStatus_STEP_STATUS_FAILED:
              mark = rml::icon("triangle", "tone-warning");
              cls += " failed";
              break;
            default:
              mark = rml::icon("trend_flat", "dim");
              cls += " pending";
              break;
          }
          s += "<div class=\"" + cls + "\">" + mark + "<span>" + e(st.label) + "</span></div>";
        }
        return s + "</div>";
      }
      case cadence_cards_v1_Node_toggle_tag: {
        // the state's whole word (the device's own, so an optimistic flip changes it with the switch) and the
        // switch at the row's end; the server's label stays the card's fallback and the screen reader's
        const auto& t = *n.kind.toggle;
        const bool on = val(t.on);
        std::string s = "<div class=\"ctoggle toggle" + std::string(t.action != nullptr ? " f" : "") + (on ? " on" : "") + "\"" +
                        a("data-label", str(t.label));
        if (t.action != nullptr) s += a("data-action", str(t.action->id)) + a("data-confirm", str(t.action->confirm)) + " data-kind=\"toggle\"";
        s += ">";
        if (t.action != nullptr) s += "<div class=\"glow\"/><div class=\"ring\"/>";
        return s + "<div class=\"sdot\"/><span class=\"won\">[[cards.state_on]]</span><span class=\"woff\">[[cards.state_off]]</span>" +
               "<div class=\"switch\"><div class=\"knob\"/></div><div class=\"pending-line\"/></div>";
      }
      case cadence_cards_v1_Node_button_tag:
        return button(*n.kind.button);
      case cadence_cards_v1_Node_keyvalue_tag: {
        std::string s = "<div class=\"ckv\">";
        for (pb_size_t i = 0; i < n.kind.keyvalue->items_count; ++i) {
          const auto& kv = n.kind.keyvalue->items[i];
          s += "<div class=\"kvrow\"><div class=\"kvkey\">" + e(kv.key) + "</div><div class=\"kvval\">" + e(kv.value) +
               "</div></div>";
        }
        return s + "</div>";
      }
      case cadence_cards_v1_Node_badge_tag:
        return badgeOf(*n.kind.badge);
      case cadence_cards_v1_Node_media_tile_tag: {
        const auto& t = *n.kind.media_tile;
        rml::Card c;
        const auto aspect = t.image != nullptr ? val(t.image->aspect) : cadence_cards_v1_Aspect_ASPECT_WIDE;
        c.kind = aspect == cadence_cards_v1_Aspect_ASPECT_POSTER   ? rml::CardKind::Poster
                 : aspect == cadence_cards_v1_Aspect_ASPECT_SQUARE ? rml::CardKind::Square
                                                                   : rml::CardKind::Wide;
        c.title = e(t.title);
        c.meta = e(t.subtitle);
        if (t.image != nullptr) c.art = art(t.image->ref);
        for (pb_size_t i = 0; i < t.badges_count; ++i) c.badges.push_back(badgeOf(t.badges[i]));
        // the badges take the meta row: the subtitle (whose it is, a music result's artist) goes under them
        if (!c.badges.empty()) c.meta2 = std::move(c.meta);
        if (t.action != nullptr) c.action = std::string(str(t.action->id));
        for (pb_size_t i = 0; i < t.secondary_count; ++i) {
          const auto& s = t.secondary[i];
          if (s.action == nullptr) continue;
          auto field = [](std::string_view v) {
            std::string out(v);
            std::replace(out.begin(), out.end(), '|', '/');
            std::replace(out.begin(), out.end(), ';', ',');
            return out;
          };
          if (!c.actions.empty()) c.actions += ";";
          c.actions += field(str(s.action->id)) + "|" + esc(field(str(s.label))) + "|" + field(str(s.icon));
        }
        return rml::card(c);
      }
      case cadence_cards_v1_Node_paged_list_tag: {
        const auto& l = *n.kind.paged_list;
        std::string s = "<div class=\"clist\">" + nodes(l.items, l.items_count);
        if (l.more != nullptr && l.more->action != nullptr) {
          rml::ListItem more;
          more.title = l.more->label != nullptr ? e(l.more->label) : std::string("[[common.show_more]]");
          more.trailIcon = "chevron_down";
          more.action = std::string(str(l.more->action->id));
          s += rml::listItem(more);
        }
        return s + "</div>";
      }
      case cadence_cards_v1_Node_divider_tag:
        return "<div class=\"cdivider\"/>";
      case cadence_cards_v1_Node_sparkline_tag: {
        const auto& sp = *n.kind.sparkline;
        double lo = sp.points[0], hi = sp.points[0];
        for (pb_size_t i = 0; i < sp.points_count; ++i) {
          lo = std::min(lo, sp.points[i]);
          hi = std::max(hi, sp.points[i]);
        }
        std::string bytes;
        for (pb_size_t i = 0; i < sp.points_count; ++i) {
          const double f = hi > lo ? (sp.points[i] - lo) / (hi - lo) : 0.5;
          bytes.push_back(static_cast<char>(static_cast<unsigned char>(std::lround(f * 255))));
        }
        std::string s = "<div class=\"cspark\">";
        if (sp.label != nullptr) s += "<div class=\"cmeta\">" + e(sp.label) + "</div>";
        return s + "<div class=\"spark\" style=\"decorator: image(&quot;gen:spark:" + hexOf(bytes) + "&quot;);\"/></div>";
      }
      case cadence_cards_v1_Node_link_tag: {
        const auto& l = *n.kind.link;
        std::string s = "<div class=\"clink f\"" + actionAttrs(l.action) + a("data-cite", val(l.cite) ? std::to_string(val(l.cite)) : "") +
                        "><div class=\"glow\"/><div class=\"ring\"/>";
        if (val(l.cite) != 0) s += "<div class=\"cite\">" + std::to_string(val(l.cite)) + "</div>";
        s += "<div class=\"ltexts\"><div class=\"ldomain\">";
        if (l.favicon != nullptr) {
          s += artBox("span", l.favicon->ref, "favicon");
        }
        s += e(l.domain);
        if (l.time != nullptr) s += " · " + e(l.time);
        s += "</div><div class=\"ltitle\">" + e(l.title) + "</div>";
        if (l.snippet != nullptr) s += "<div class=\"lsnippet\">" + e(l.snippet) + "</div>";
        s += "</div>";
        if (l.thumbnail != nullptr) {
          s += artBox("div", l.thumbnail->ref, "lthumb");
        }
        return s + "</div>";
      }
      case cadence_cards_v1_Node_post_tag:
        return post(*n.kind.post, false);
      case cadence_cards_v1_Node_slider_tag: {
        const auto& sl = *n.kind.slider;
        rml::Slider s;
        s.label = e(sl.label);
        s.value = val(sl.value);
        s.min = val(sl.min);
        s.max = val(sl.max);
        s.step = val(sl.step);
        s.valueLabel = sl.value_label != nullptr ? e(sl.value_label) : esc(rml::sliderLabel(s.value, str(sl.unit)));
        if (sl.action != nullptr) s.action = std::string(str(sl.action->id));
        std::string extra = a("data-server-value", fmt(s.value));
        if (sl.timeline != nullptr) {
          // a playback position: the device runs it from its anchor and draws the time, never the server's label
          Timeline t;
          t.value = s.value;
          t.min = s.min;
          t.max = s.max;
          t.anchorAtMs = val(sl.timeline->anchor_at_ms);
          t.rate = val(sl.timeline->rate);
          s.value = std::floor(t.at(opt.nowEpochMs));
          s.valueLabel = esc(timelineText(s.value, s.max));
          s.cls = "timeline";
          s.focusable = sl.action != nullptr;
          extra += a("data-timeline", "1") + a("data-anchor", std::to_string(t.anchorAtMs)) + a("data-rate", fmt(t.rate));
        }
        std::string out = rml::slider(s);
        out.insert(out.find('>'), a("data-unit", str(sl.unit)) + extra);
        return out;
      }
      case cadence_cards_v1_Node_qr_tag: {
        const auto& q = *n.kind.qr;
        std::string s = "<div class=\"cqr\"><div class=\"qr\" style=\"decorator: image(&quot;gen:qr:" +
                        hexOf(str(q.payload)) + "&quot;);\"/>";
        if (q.caption != nullptr) s += "<div class=\"cmeta\">" + e(q.caption) + "</div>";
        return s + "</div>";
      }
      case cadence_cards_v1_Node_code_tag: {
        const auto& c = *n.kind.code;
        std::string s = "<div class=\"ccodebox\">";
        if (c.label != nullptr) s += "<div class=\"cmeta\">" + e(c.label) + "</div>";
        return s + "<div class=\"ccode f\" data-kind=\"scroll\"><div class=\"glow\"/><div class=\"ring\"/><div class=\"ctext mono\">" +
               e(c.text) + "</div></div><div class=\"codemore cmeta hidden\"/></div>";
      }
      case cadence_cards_v1_Node_choices_tag: {
        const auto& c = *n.kind.choices;
        const bool multiple = val(c.multiple);
        const std::string act = c.action != nullptr ? std::string(str(c.action->id)) : std::string();
        std::string s = "<div class=\"cchoices\"" + a("data-choice-action", act) +
                        (multiple ? " data-multiple=\"1\" data-min=\"" + std::to_string(std::max(1u, val(c.min))) +
                                        "\" data-max=\"" + std::to_string(val(c.max) != 0 ? val(c.max) : c.options_count) + "\""
                                  : std::string()) +
                        ">";
        for (pb_size_t i = 0; i < c.options_count; ++i) {
          const auto& o = c.options[i];
          if (multiple) {
            std::string chip = rml::chip(e(o.label), {}, false, "choice");
            chip.insert(chip.find('>'), a("data-value", str(o.id)) + " data-kind=\"check\"");
            s += chip;
          } else {
            rml::Button b;
            b.label = e(o.label);
            b.action = act;
            std::string btn = rml::button(b);
            btn.insert(btn.find('>'), a("data-value", str(o.id)));
            s += btn;
          }
        }
        if (multiple) {
          rml::Button submit;
          submit.label = c.submit_label != nullptr ? e(c.submit_label) : std::string("[[cards.submit]]");
          submit.action = act;
          submit.primary = true;
          std::string btn = rml::button(submit);
          btn.insert(btn.find('>'), " data-submit=\"1\"");
          s += btn;
        }
        return s + "</div>";
      }
      default:
        return {};
    }
  }

  std::string button(const cadence_cards_v1_Button& b) {
    rml::Button rb;
    rb.label = e(b.label);
    const auto ic = str(b.icon);
    if (rml::hasIcon(ic)) rb.icon = std::string(ic);
    rb.primary = val(b.primary);
    if (b.action != nullptr) rb.action = std::string(str(b.action->id));
    std::string s = rml::button(rb);
    if (b.action != nullptr && b.action->confirm != nullptr) s.insert(s.find('>'), a("data-confirm", str(b.action->confirm)));
    return s;
  }

  std::string card(const cadence_cards_v1_Card& c) {
    const auto& root = *c.root->kind.card;
    const std::string tone = toneName(root.tone);
    std::string s = "<div class=\"ccard tn-" + tone + std::string(opt.panel ? " panel" : "") + kindClass(root) + "\"" +
                    a("data-card", str(c.id)) + " data-revision=\"" + std::to_string(val(c.revision)) +
                    "\"><div class=\"cstripe stripe-" + tone + "\"/><div class=\"chead\">";
    if (root.icon != nullptr) {
      const auto name = str(root.icon);
      s += "<div class=\"ctile\">" + rml::icon(rml::hasIcon(name) ? name : std::string_view("unknown"), "cicon tone-" + tone) +
           "</div>";
    }
    s += "<div class=\"chtexts\"><div class=\"ctitle\">" + e(root.title) + "</div>";
    if (root.subtitle != nullptr) s += "<div class=\"csub\">" + e(root.subtitle) + "</div>";
    s += "</div></div><div class=\"cbody\">" + nodes(root.children, root.children_count) + "</div>";
    if (root.actions_count > 0) {
      s += "<div class=\"cactions\">";
      for (pb_size_t i = 0; i < root.actions_count; ++i) s += button(root.actions[i]);
      s += "</div>";
    }
    return s + "</div>";
  }
};

}  // namespace

std::string countdownText(std::int64_t endsAtMs, std::int64_t nowEpochMs, cadence_cards_v1_CountdownFormat format) {
  const std::int64_t left = std::max<std::int64_t>(0, (endsAtMs - nowEpochMs + 999) / 1000);
  char buf[32];
  if (format == cadence_cards_v1_CountdownFormat_COUNTDOWN_FORMAT_HH_MM_SS) {
    std::snprintf(buf, sizeof buf, "%02lld:%02lld:%02lld", static_cast<long long>(left / 3600),
                  static_cast<long long>(left / 60 % 60), static_cast<long long>(left % 60));
  } else {
    std::snprintf(buf, sizeof buf, "%02lld:%02lld", static_cast<long long>(left / 60), static_cast<long long>(left % 60));
  }
  return buf;
}

std::string renderFallback(std::string_view fallbackText, std::string_view id) {
  return "<div class=\"ccard fallback\"" + a("data-card", id) +
         "><div class=\"cstripe stripe-neutral\"/><div class=\"cbody\"><div class=\"ctext t-body\">" + esc(fallbackText) +
         "</div><div class=\"ctext t-body cmeta\">[[cards.unavailable]]</div></div></div>";
}

std::string render(const cadence_cards_v1_Card& card, const RenderOptions& options) {
  const auto v = check(card);
  if (!v.ok()) return renderFallback(str(card.fallback_text), str(card.id));
  Renderer r{options};
  return r.card(card);
}

std::string renderPage(const cadence_cards_v1_Page& page, const RenderOptions& options) {
  std::string s = "<div class=\"cpage\"" + a("data-page", str(page.id)) + ">";
  const auto n = std::min<pb_size_t>(page.cards_count, static_cast<pb_size_t>(kMaxPageCards));
  for (pb_size_t i = 0; i < n; ++i) s += render(page.cards[i], options);
  if (page.next != nullptr) {
    rml::ListItem more;
    more.title = "[[common.show_more]]";
    more.trailIcon = "chevron_down";
    more.action = std::string(str(page.next->id));
    std::string item = rml::listItem(more);
    item.insert(item.find('>'), " data-page-next=\"1\"");
    s += item;
  }
  return s + "</div>";
}

}  // namespace cadence::screen::ui::cards

#include "ui/cards/model.h"

#include <pb_decode.h>
#include <pb_encode.h>

#include <algorithm>
#include <array>
#include <cstdlib>

namespace cadence::screen::ui::cards {

namespace {

constexpr std::array<std::string_view, 28> kPrimitives{
    "card",  "row",      "column",  "grid",   "text",       "long_text",  "icon",    "image",     "state", "value",
    "meter", "progress", "countdown", "steps", "toggle",     "button",     "keyvalue", "badge",    "media_tile",
    "paged_list", "divider", "sparkline", "link", "post", "slider", "qr", "code", "choices"};
constexpr std::array<std::string_view, 7> kFields{"link.cite",     "long_text.tail",   "post.note",      "post.quote",
                                                  "post.reply_to", "post.reposted_by", "slider.timeline"};

struct Walk {
  int nodes = 0;
  int focusables = 0;
  int depth = 0;
  std::string reason;

  bool fail(const char* r) {
    if (reason.empty()) reason = r;
    return false;
  }
  bool text(const char* s, std::size_t max, bool required) {
    const auto n = runes(str(s));
    if (required && n == 0) return fail("required");
    if (n > max) return fail("length");
    return true;
  }
  bool node(const cadence_cards_v1_Node& n, int d);
  bool post(const cadence_cards_v1_Post& p, int d, bool quoted);
  bool children(const cadence_cards_v1_Node* kids, pb_size_t count, int d) {
    for (pb_size_t i = 0; i < count; ++i) {
      if (!node(kids[i], d + 1)) return false;
    }
    return true;
  }
};

bool Walk::post(const cadence_cards_v1_Post& p, int d, bool quoted) {
  if (!text(p.author, 80, true) || !text(p.text, 500, true)) return false;
  if (p.action != nullptr) ++focusables;
  if (p.note != nullptr && (!text(p.note->text, 500, true) || !text(p.note->label, 40, false))) return false;
  if (p.quote != nullptr) {
    if (quoted) return fail("quote_depth");
    ++nodes;
    if (d + 1 > kMaxDepth) return fail("depth");
    return post(*p.quote, d + 1, true);
  }
  return true;
}

bool Walk::node(const cadence_cards_v1_Node& n, int d) {
  ++nodes;
  depth = std::max(depth, d);
  if (d > kMaxDepth) return fail("depth");
  if (nodes > kMaxNodes) return fail("nodes");
  switch (n.which_kind) {
    case cadence_cards_v1_Node_card_tag: {
      if (d != 0) return fail("root");
      const auto& c = *n.kind.card;
      if (!text(c.title, 80, true) || !text(c.subtitle, 80, false)) return false;
      if (val(c.tone) == cadence_cards_v1_Tone_TONE_ACCENT) return fail("tone");
      if (c.actions_count > 4) return fail("actions");
      focusables += static_cast<int>(c.actions_count);
      return children(c.children, c.children_count, d);
    }
    case cadence_cards_v1_Node_row_tag:
      return children(n.kind.row->children, n.kind.row->children_count, d);
    case cadence_cards_v1_Node_column_tag:
      return children(n.kind.column->children, n.kind.column->children_count, d);
    case cadence_cards_v1_Node_grid_tag: {
      const auto cols = val(n.kind.grid->columns);
      if (cols < 2 || cols > 4) return fail("range");
      return children(n.kind.grid->children, n.kind.grid->children_count, d);
    }
    case cadence_cards_v1_Node_text_tag:
      if (n.kind.text->style == nullptr) return fail("required");
      return text(n.kind.text->text, 280, true);
    case cadence_cards_v1_Node_long_text_tag: {
      ++focusables;
      const auto l = val(n.kind.long_text->collapsed_lines);
      if (l != 0 && (l < 3 || l > 10)) return fail("range");
      return text(n.kind.long_text->text, 8000, true);
    }
    case cadence_cards_v1_Node_icon_tag: {
      const auto s = val(n.kind.icon->size);
      if (s != 0 && s != 32 && s != 48) return fail("range");
      return text(n.kind.icon->name, 32, true);
    }
    case cadence_cards_v1_Node_image_tag:
      return text(n.kind.image->ref, 100, true) && text(n.kind.image->alt, 120, true);
    case cadence_cards_v1_Node_state_tag:
      return text(n.kind.state->label, 40, true);
    case cadence_cards_v1_Node_value_tag:
      return text(n.kind.value->value, 24, true) && text(n.kind.value->unit, 12, false) &&
             text(n.kind.value->label, 40, false);
    case cadence_cards_v1_Node_meter_tag: {
      const auto& m = *n.kind.meter;
      if (!(val(m.min) < val(m.max)) || val(m.value) < val(m.min) || val(m.value) > val(m.max)) return fail("range");
      return text(m.label, 40, true) && text(m.value_label, 40, true);
    }
    case cadence_cards_v1_Node_progress_tag: {
      const auto& p = *n.kind.progress;
      if (p.which_amount == cadence_cards_v1_Progress_fraction_tag) {
        const double f = val(p.amount.fraction);
        if (!(f >= 0 && f <= 1)) return fail("range");
      } else if (p.which_amount != cadence_cards_v1_Progress_indeterminate_tag) {
        return fail("required");
      }
      return text(p.label, 40, false);
    }
    case cadence_cards_v1_Node_countdown_tag:
      if (val(n.kind.countdown->ends_at_ms) <= 0) return fail("range");
      return text(n.kind.countdown->label, 40, false);
    case cadence_cards_v1_Node_steps_tag: {
      const auto& s = *n.kind.steps;
      if (s.items_count < 1 || s.items_count > 12) return fail("items");
      for (pb_size_t i = 0; i < s.items_count; ++i) {
        if (!text(s.items[i].label, 80, true)) return false;
      }
      return true;
    }
    case cadence_cards_v1_Node_toggle_tag:
      if (n.kind.toggle->action != nullptr) ++focusables;
      return text(n.kind.toggle->label, 40, true);
    case cadence_cards_v1_Node_button_tag:
      ++focusables;
      if (n.kind.button->action == nullptr) return fail("required");
      return text(n.kind.button->label, 40, true);
    case cadence_cards_v1_Node_keyvalue_tag: {
      const auto& k = *n.kind.keyvalue;
      if (k.items_count < 1 || k.items_count > 12) return fail("items");
      for (pb_size_t i = 0; i < k.items_count; ++i) {
        if (!text(k.items[i].key, 40, true) || !text(k.items[i].value, 80, true)) return false;
      }
      return true;
    }
    case cadence_cards_v1_Node_badge_tag:
      return text(n.kind.badge->text, 24, false);
    case cadence_cards_v1_Node_media_tile_tag: {
      ++focusables;
      const auto& t = *n.kind.media_tile;
      if (t.image == nullptr || val(t.image->aspect) == cadence_cards_v1_Aspect_ASPECT_UNSPECIFIED) return fail("required");
      if (t.secondary_count > 4) return fail("actions");
      return text(t.title, 80, true) && text(t.subtitle, 80, false);
    }
    case cadence_cards_v1_Node_paged_list_tag: {
      const auto& l = *n.kind.paged_list;
      if (l.items_count > static_cast<pb_size_t>(kMaxListItems)) return fail("items");
      // one focus region that scrolls its items, plus its more row
      const int before = focusables;
      for (pb_size_t i = 0; i < l.items_count; ++i) {
        const auto k = l.items[i].which_kind;
        if (k != cadence_cards_v1_Node_post_tag && k != cadence_cards_v1_Node_media_tile_tag &&
            k != cadence_cards_v1_Node_link_tag) {
          return fail("list_item");
        }
        if (!node(l.items[i], d + 1)) return false;
      }
      focusables = before + 1 + (l.more != nullptr ? 1 : 0);
      return true;
    }
    case cadence_cards_v1_Node_divider_tag:
      return true;
    case cadence_cards_v1_Node_sparkline_tag: {
      const auto& s = *n.kind.sparkline;
      if (s.points_count < 2 || s.points_count > 64) return fail("items");
      return text(s.label, 40, false);
    }
    case cadence_cards_v1_Node_link_tag: {
      ++focusables;
      const auto& l = *n.kind.link;
      if (val(l.cite) > 99) return fail("range");
      return text(l.domain, 64, true) && text(l.title, 120, true) && text(l.snippet, 240, false) &&
             text(l.time, 40, false);
    }
    case cadence_cards_v1_Node_post_tag:
      return post(*n.kind.post, d, false);
    case cadence_cards_v1_Node_slider_tag: {
      const auto& s = *n.kind.slider;
      const double lo = val(s.min), hi = val(s.max), v = val(s.value), st = val(s.step);
      // a timeline's value is a position the device clamps (a session a moment past its length still draws), and
      // without an action it is shown alone, not focusable
      const bool timeline = s.timeline != nullptr;
      if (!(lo < hi) || v < lo || (v > hi && !timeline) || !(st > 0) || st > hi - lo) return fail("range");
      if (s.action == nullptr && !timeline) return fail("required");
      if (s.action != nullptr) ++focusables;
      return text(s.label, 40, true) && text(s.unit, 12, false);
    }
    case cadence_cards_v1_Node_qr_tag:
      return text(n.kind.qr->payload, 512, true) && text(n.kind.qr->caption, 80, false);
    case cadence_cards_v1_Node_code_tag:
      ++focusables;
      return text(n.kind.code->text, 4000, true) && text(n.kind.code->label, 40, false);
    case cadence_cards_v1_Node_choices_tag: {
      const auto& c = *n.kind.choices;
      if (c.options_count < 1 || c.options_count > 8) return fail("items");
      focusables += static_cast<int>(c.options_count) + (val(c.multiple) ? 1 : 0);
      if (c.action == nullptr) return fail("required");
      for (pb_size_t i = 0; i < c.options_count; ++i) {
        if (!text(c.options[i].id, 64, true) || !text(c.options[i].label, 40, true)) return false;
      }
      return true;
    }
    default:
      // an empty node, or a primitive this build does not know (a newer minor version)
      return fail("unknown");
  }
}

}  // namespace

std::size_t runes(std::string_view s) {
  return static_cast<std::size_t>(
      std::count_if(s.begin(), s.end(), [](char c) { return (static_cast<unsigned char>(c) & 0xc0) != 0x80; }));
}

std::vector<std::uint32_t> versions() { return {kVersion}; }

std::vector<std::string> features() {
  std::vector<std::string> out(kPrimitives.begin(), kPrimitives.end());
  out.insert(out.end(), kFields.begin(), kFields.end());
  return out;
}

bool knownFeature(std::string_view f) {
  return std::find(kPrimitives.begin(), kPrimitives.end(), f) != kPrimitives.end() ||
         std::find(kFields.begin(), kFields.end(), f) != kFields.end();
}

void CardDeleter::operator()(cadence_cards_v1_Card* c) const {
  if (c == nullptr) return;
  pb_release(cadence_cards_v1_Card_fields, c);
  delete c;
}

void PageDeleter::operator()(cadence_cards_v1_Page* p) const {
  if (p == nullptr) return;
  pb_release(cadence_cards_v1_Page_fields, p);
  delete p;
}

CardPtr decodeCard(std::span<const std::uint8_t> bytes) {
  if (bytes.size() > kMaxCardBytes) return nullptr;
  CardPtr c(new cadence_cards_v1_Card{});
  auto stream = pb_istream_from_buffer(bytes.data(), bytes.size());
  if (!pb_decode(&stream, cadence_cards_v1_Card_fields, c.get())) return nullptr;
  return c;
}

PagePtr decodePage(std::span<const std::uint8_t> bytes) {
  if (bytes.size() > kMaxPageBytes) return nullptr;
  PagePtr p(new cadence_cards_v1_Page{});
  auto stream = pb_istream_from_buffer(bytes.data(), bytes.size());
  if (!pb_decode(&stream, cadence_cards_v1_Page_fields, p.get())) return nullptr;
  return p;
}

std::size_t encodedSize(const cadence_cards_v1_Card& card) {
  std::size_t size = 0;
  if (!pb_get_encoded_size(&size, cadence_cards_v1_Card_fields, &card)) return 0;
  return size;
}

CardPtr copyCard(const cadence_cards_v1_Card& card) {
  const std::size_t size = encodedSize(card);
  if (size == 0 || size > kMaxCardBytes) return nullptr;
  std::vector<std::uint8_t> buf(size);
  auto out = pb_ostream_from_buffer(buf.data(), buf.size());
  if (!pb_encode(&out, cadence_cards_v1_Card_fields, &card)) return nullptr;
  return decodeCard(buf);
}

Verdict check(const cadence_cards_v1_Card& card) {
  Verdict v;
  if (val(card.version) != kVersion) {
    v.reason = "version";
    return v;
  }
  if (runes(str(card.fallback_text)) == 0 || runes(str(card.fallback_text)) > 280) {
    v.reason = "fallback_text";
    return v;
  }
  for (pb_size_t i = 0; i < card.features_count; ++i) {
    // the features -> fallback rule: proto skips unknown fields silently, so a feature the device lacks means
    // the card is drawn as its fallback
    if (!knownFeature(str(card.features[i]))) {
      v.reason = "feature";
      return v;
    }
  }
  if (card.root == nullptr || card.root->which_kind != cadence_cards_v1_Node_card_tag) {
    v.reason = "root";
    return v;
  }
  if (encodedSize(card) > kMaxCardBytes) {
    v.reason = "bytes";
    return v;
  }
  Walk w;
  w.node(*card.root, 0);
  v.nodes = w.nodes;
  v.focusables = w.focusables;
  v.depth = w.depth;
  v.reason = w.reason;
  if (v.reason.empty() && w.focusables > kMaxFocusables) v.reason = "focusables";
  return v;
}

}  // namespace cadence::screen::ui::cards

#include "ui/cards/content.h"

#include <algorithm>

namespace cadence::screen::ui::cards {

namespace {

const cadence_cards_v1_CardBlock* block(const cadence_cards_v1_Card& card) {
  if (card.root == nullptr || card.root->which_kind != cadence_cards_v1_Node_card_tag) return nullptr;
  return card.root->kind.card;
}

// found things under n: a paged list's items, a tile or an image; -1 when n holds none
int found(const cadence_cards_v1_Node& n, int depth) {
  switch (n.which_kind) {
    case cadence_cards_v1_Node_paged_list_tag:
      return static_cast<int>(n.kind.paged_list->items_count);
    case cadence_cards_v1_Node_media_tile_tag:
    case cadence_cards_v1_Node_post_tag:
    case cadence_cards_v1_Node_link_tag:
      return 1;
    case cadence_cards_v1_Node_image_tag:
      return n.kind.image->aspect != nullptr && *n.kind.image->aspect == cadence_cards_v1_Aspect_ASPECT_AVATAR ? -1 : 1;
    case cadence_cards_v1_Node_row_tag:
    case cadence_cards_v1_Node_column_tag:
    case cadence_cards_v1_Node_grid_tag: {
      if (depth >= 2) return -1;
      const cadence_cards_v1_Node* kids = nullptr;
      pb_size_t count = 0;
      if (n.which_kind == cadence_cards_v1_Node_row_tag) {
        kids = n.kind.row->children;
        count = n.kind.row->children_count;
      } else if (n.which_kind == cadence_cards_v1_Node_column_tag) {
        kids = n.kind.column->children;
        count = n.kind.column->children_count;
      } else {
        kids = n.kind.grid->children;
        count = n.kind.grid->children_count;
      }
      int total = -1;
      for (pb_size_t i = 0; i < count; ++i) {
        const int f = found(kids[i], depth + 1);
        if (f >= 0) total = total < 0 ? f : total + f;
      }
      return total;
    }
    default:
      return -1;
  }
}

// the server's found music, videos and a catalog item's episodes: a card of the media icons whose list is a column
// of entries (a column each: a title and a meta line); their tool output has no image and no action a tile needs
int entriesIn(const cadence_cards_v1_CardBlock& b) {
  const auto icon = str(b.icon);
  if (icon != "music" && icon != "film") return -1;
  for (pb_size_t i = 0; i < b.children_count; ++i) {
    const auto& n = b.children[i];
    if (n.which_kind != cadence_cards_v1_Node_column_tag || n.kind.column->children_count == 0) continue;
    bool entries = true;
    for (pb_size_t k = 0; k < n.kind.column->children_count; ++k) {
      entries = entries && n.kind.column->children[k].which_kind == cadence_cards_v1_Node_column_tag;
    }
    if (entries) return static_cast<int>(n.kind.column->children_count);
  }
  return -1;
}

int foundIn(const cadence_cards_v1_CardBlock& b) {
  if (const int e = entriesIn(b); e > 0) return e;
  int total = -1;
  for (pb_size_t i = 0; i < b.children_count; ++i) {
    const int f = found(b.children[i], 0);
    if (f >= 0) total = total < 0 ? f : total + f;
  }
  return total;
}

}  // namespace

namespace {

// a control (a toggle, a slider, a choice) or a state (a play starting, a light on) under n, at most two levels down
bool controlOrState(const cadence_cards_v1_Node& n, int depth) {
  switch (n.which_kind) {
    case cadence_cards_v1_Node_toggle_tag:
    case cadence_cards_v1_Node_slider_tag:
    case cadence_cards_v1_Node_choices_tag:
    case cadence_cards_v1_Node_state_tag:
    // a spinner says a call still runs (a play loading): the chat's, cover or not
    case cadence_cards_v1_Node_progress_tag:
      return true;
    case cadence_cards_v1_Node_row_tag:
    case cadence_cards_v1_Node_column_tag: {
      if (depth >= 2) return false;
      const bool row = n.which_kind == cadence_cards_v1_Node_row_tag;
      const auto* kids = row ? n.kind.row->children : n.kind.column->children;
      const auto count = row ? n.kind.row->children_count : n.kind.column->children_count;
      for (pb_size_t i = 0; i < count; ++i) {
        if (controlOrState(kids[i], depth + 1)) return true;
      }
      return false;
    }
    default:
      return false;
  }
}

}  // namespace

bool isContent(const cadence_cards_v1_Card& card) {
  const auto* b = block(card);
  if (b == nullptr) return false;
  // a control or a state keeps the card in the chat, whatever else it shows: a play's card with its poster is the
  // state of the screen, not a found thing
  for (pb_size_t i = 0; i < b->children_count; ++i) {
    if (controlOrState(b->children[i], 0)) return false;
  }
  return foundIn(*b) > 0;
}

Summary summarize(const cadence_cards_v1_Card& card) {
  Summary s;
  if (const auto* b = block(card)) {
    s.title = std::string(str(b->title));
    s.icon = std::string(str(b->icon));
    s.count = std::max(0, foundIn(*b));
  }
  return s;
}

}  // namespace cadence::screen::ui::cards

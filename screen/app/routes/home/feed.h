#pragma once

#include <algorithm>
#include <cstddef>

// home's feed column, pure (the unit tests pin it): where the focused row sits and which rows show around it
namespace cadence::screen::routes::feed {

// the column's scroll (px) that puts the focused row's top at anchorTop, the line under the hero's name and details,
// so every row lands in the same place and the row below shows the start of itself. rowTop and anchorTop are layout
// px of the same frame; the column never moves down past its own top
inline float scrollFor(float rowTop, float anchorTop) { return std::max(0.0f, rowTop - anchorTop); }

// rows above the focused one step aside (the hero's details hold that space), the focused one and those below show
inline bool hiddenAbove(std::ptrdiff_t index, std::ptrdiff_t focused) { return focused >= 0 && index < focused; }

}  // namespace cadence::screen::routes::feed

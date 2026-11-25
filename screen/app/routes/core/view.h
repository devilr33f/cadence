#pragma once

#include <RmlUi/Core/Element.h>
#include <RmlUi/Core/ElementDocument.h>

#include <string>
#include <string_view>

// small dom helpers the routes share: streamed results go in without rebuilding the page, so focus and scroll stay
namespace cadence::screen::routes::view {

// rml parsed into new children of parent, before `before` (null: at the end); the first one added, or null
Rml::Element* append(Rml::Element* parent, const std::string& rml, Rml::Element* before = nullptr);
// the element's children replaced by rml
void fill(Rml::Element* e, const std::string& rml);
// the element of that id, null without a page
Rml::Element* byId(Rml::ElementDocument* doc, const std::string& id);

// the focus treatment's children (ring, glow, hold), for markup the library has no builder for
std::string focusDecor();

// a utf-8 string's code points
std::size_t codepoints(std::string_view s);
// the string less its last code point
std::string dropLast(std::string_view s);
// whether it holds a cyrillic letter
bool cyrillic(std::string_view s);

}  // namespace cadence::screen::routes::view

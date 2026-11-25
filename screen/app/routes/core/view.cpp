#include "app/routes/core/view.h"

#include <vector>

namespace cadence::screen::routes::view {

Rml::Element* append(Rml::Element* parent, const std::string& rml, Rml::Element* before) {
  if (parent == nullptr) return nullptr;
  auto* doc = parent->GetOwnerDocument();
  if (doc == nullptr) return nullptr;
  // rmlui parses inner rml into an element of its own; its children then move under parent
  auto holder = doc->CreateElement("div");
  holder->SetInnerRML(rml);
  Rml::Element* first = nullptr;
  while (holder->GetNumChildren() > 0) {
    auto child = holder->RemoveChild(holder->GetChild(0));
    auto* added = before != nullptr ? parent->InsertBefore(std::move(child), before) : parent->AppendChild(std::move(child));
    if (first == nullptr) first = added;
  }
  return first;
}

void fill(Rml::Element* e, const std::string& rml) {
  if (e != nullptr) e->SetInnerRML(rml);
}

Rml::Element* byId(Rml::ElementDocument* doc, const std::string& id) {
  return doc != nullptr ? doc->GetElementById(id) : nullptr;
}

std::string focusDecor() { return "<div class=\"glow\"/><div class=\"ring\"/><div class=\"hold\"/>"; }

std::size_t codepoints(std::string_view s) {
  std::size_t n = 0;
  for (char ch : s) {
    if ((static_cast<unsigned char>(ch) & 0xC0) != 0x80) n++;
  }
  return n;
}

std::string dropLast(std::string_view s) {
  if (s.empty()) return {};
  std::size_t i = s.size() - 1;
  while (i > 0 && (static_cast<unsigned char>(s[i]) & 0xC0) == 0x80) i--;
  return std::string(s.substr(0, i));
}

bool cyrillic(std::string_view s) {
  for (std::size_t i = 0; i + 1 < s.size(); ++i) {
    const auto c = static_cast<unsigned char>(s[i]);
    // U+0400-U+04FF: lead bytes 0xD0-0xD3
    if (c >= 0xD0 && c <= 0xD3) return true;
  }
  return false;
}

}  // namespace cadence::screen::routes::view

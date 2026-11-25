#pragma once

#include <cstdint>
#include <span>
#include <string>
#include <vector>

// the system's faces for what the ui's own (inter, cut to latin, cyrillic and punctuation) lacks: one cjk face and
// one emoji face, the first of each the platform has (android's /system/fonts, a linux distribution's noto, macos's
// own). they are mapped read-only, never read whole: the kernel pages in what freetype touches (a cjk face's cmap and
// the glyphs drawn), so a 20 MB face costs no 20 MB, and nothing is bundled in the app. mapped once a process and
// kept: rmlui's font engine holds the bytes for as long as it runs, across ui reloads
namespace cadence::screen::ui::rml {

struct FallbackFont {
  std::string path;
  std::span<const std::uint8_t> bytes;
};

// in rmlui's fallback order (cjk, then emoji); empty where the platform has none
const std::vector<FallbackFont>& fallbackFonts();

// the candidates, in order, per kind: tests and the log read them
std::vector<std::string> cjkCandidates();
std::vector<std::string> emojiCandidates();

}  // namespace cadence::screen::ui::rml

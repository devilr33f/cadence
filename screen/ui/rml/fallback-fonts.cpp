#include "ui/rml/fallback-fonts.h"

#if defined(__unix__) || defined(__APPLE__)
#include <fcntl.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>
#endif

#include <cstdlib>

namespace cadence::screen::ui::rml {

namespace {

// a font file mapped read-only; empty when it is missing or not a font's size
std::span<const std::uint8_t> map(const std::string& path) {
#if defined(__unix__) || defined(__APPLE__)
  const int fd = ::open(path.c_str(), O_RDONLY | O_CLOEXEC);
  if (fd < 0) return {};
  struct stat st {};
  if (::fstat(fd, &st) != 0 || !S_ISREG(st.st_mode) || st.st_size < 1024 || st.st_size > (256 << 20)) {
    ::close(fd);
    return {};
  }
  void* p = ::mmap(nullptr, static_cast<std::size_t>(st.st_size), PROT_READ, MAP_PRIVATE, fd, 0);
  ::close(fd);
  if (p == MAP_FAILED) return {};
  return {static_cast<const std::uint8_t*>(p), static_cast<std::size_t>(st.st_size)};
#else
  (void)path;
  return {};
#endif
}

void addFirst(const std::vector<std::string>& candidates, std::vector<FallbackFont>& out) {
  for (const auto& path : candidates) {
    if (auto bytes = map(path); !bytes.empty()) {
      out.push_back({path, bytes});
      return;
    }
  }
}

}  // namespace

std::vector<std::string> cjkCandidates() {
  return {
      // android: noto cjk since 7, droid's fallback before it (face 0 of the collection is the japanese cut, which
      // draws chinese, japanese and korean)
      "/system/fonts/NotoSansCJK-Regular.ttc",
      "/system/fonts/NotoSansSC-Regular.otf",
      "/system/fonts/DroidSansFallbackFull.ttf",
      "/system/fonts/DroidSansFallback.ttf",
      // linux distributions
      "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
      "/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc",
      "/usr/share/fonts/google-noto-cjk/NotoSansCJK-Regular.ttc",
      "/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf",
      // macos
      "/System/Library/Fonts/Hiragino Sans GB.ttc",
      "/System/Library/Fonts/STHeiti Light.ttc",
      "/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
  };
}

std::vector<std::string> emojiCandidates() {
  return {
      // android's colour bitmaps (cbdt); freetype draws them as rgba and rmlui scales the strike down
      "/system/fonts/NotoColorEmojiLegacy.ttf",
      "/system/fonts/NotoColorEmoji.ttf",
      "/usr/share/fonts/truetype/noto/NotoColorEmoji.ttf",
      "/usr/share/fonts/noto/NotoColorEmoji.ttf",
      "/usr/share/fonts/google-noto-emoji/NotoColorEmoji.ttf",
      "/System/Library/Fonts/Apple Color Emoji.ttc",
  };
}

const std::vector<FallbackFont>& fallbackFonts() {
  static const std::vector<FallbackFont> fonts = [] {
    std::vector<FallbackFont> out;
    // CADENCE_SCREEN_NO_SYSTEM_FONTS: the gallery's golden images and tests draw the same anywhere
    if (std::getenv("CADENCE_SCREEN_NO_SYSTEM_FONTS") != nullptr) return out;
    addFirst(cjkCandidates(), out);
    addFirst(emojiCandidates(), out);
    return out;
  }();
  return fonts;
}

}  // namespace cadence::screen::ui::rml

#pragma once

#include "audio/ogg-opus.h"

#include <cstdint>
#include <map>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::audiod {

// the spoken prompts under one directory, <dir>/<lang>/<id>.opus, each read and decoded the first time it is asked
// for and kept, a failure too, until the directory changes. only that path is opened: an id or a lang outside its
// token pattern opens nothing, and the file must be a regular one of at most kMaxPromptFileBytes. one thread
class Prompts {
 public:
  struct Loaded {
    // null when the prompt cannot play; reason then says why (missing, invalid, unconfigured, read, or the decoder's)
    const std::vector<std::int16_t>* pcm = nullptr;
    std::string_view reason;
  };

  // "" leaves every prompt missing (unconfigured); a new directory forgets what the old one held
  void setDir(std::string_view dir);
  Loaded get(std::string_view lang, std::string_view id);

  // [a-z_], 1 to 32
  static bool validId(std::string_view id);
  // [a-z], 2 or 3
  static bool validLang(std::string_view lang);

 private:
  struct Entry {
    std::optional<std::vector<std::int16_t>> pcm;
    std::string reason;
  };
  Entry load(std::string_view lang, std::string_view id) const;

  std::string dir_;
  std::map<std::string, Entry, std::less<>> cache_;
};

}  // namespace cadence::audiod

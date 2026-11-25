#pragma once

#include <cstdint>
#include <set>
#include <string>
#include <vector>

namespace cadence::render::viz {

// the curated sources (shader presets, milkdrop presets by name, classic styles) less the ones
// this device found too slow, kept in a local file of one name a line so a skip outlives the app
class Playlist {
 public:
  // reads slow_file when it exists; an empty path keeps the slow list in memory only
  Playlist(std::vector<std::string> names, std::string slow_file);

  [[nodiscard]] bool empty() const;
  [[nodiscard]] const std::string& current() const;
  // ← and →; each skips slow ones and wraps
  const std::string& next();
  const std::string& prev();
  // makes a name current; false when it is not in the list or slow
  bool select(const std::string& name);
  // a new track: a different one, picked from seed
  const std::string& shuffle(std::uint64_t seed);
  // the current one is too slow here: remembered (appended to the file) and passed over from now on
  void mark_slow();
  // the current one failed to start (a milkdrop preset projectM cannot read or compile): passed over for this run
  // alone, as a driver update may fix it, and the next usable one made current
  void drop();

  [[nodiscard]] const std::set<std::string>& slow() const { return slow_; }
  // only names starting with one of these prefixes ("drive:", "scene:") play; none, or a family with nothing usable
  // here, plays them all. the current one moves into the family when it was outside it
  void set_family(std::vector<std::string> prefixes);
  [[nodiscard]] bool in_family(const std::string& name) const;
  // another usable name than the current one
  [[nodiscard]] bool has_other() const;

 private:
  [[nodiscard]] bool usable(std::size_t i) const;
  [[nodiscard]] bool family_has_usable() const;
  const std::string& step(int dir);

  std::vector<std::string> names_;
  std::string slow_file_;
  std::set<std::string> slow_;
  std::set<std::string> dropped_;
  std::size_t at_ = 0;
  std::vector<std::string> family_;
};

// the names in a list file: one a line, # comments and blank lines skipped
std::vector<std::string> read_list(const std::string& path);

}  // namespace cadence::render::viz

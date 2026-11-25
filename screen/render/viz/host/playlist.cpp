#include "playlist.h"

#include <fstream>

namespace cadence::render::viz {

namespace {
const std::string kNone;
}

std::vector<std::string> read_list(const std::string& path) {
  std::vector<std::string> out;
  std::ifstream in(path);
  std::string line;
  while (std::getline(in, line)) {
    if (!line.empty() && line.back() == '\r') line.pop_back();
    if (line.empty() || line.front() == '#') continue;
    out.push_back(line);
  }
  return out;
}

Playlist::Playlist(std::vector<std::string> names, std::string slow_file)
    : names_(std::move(names)), slow_file_(std::move(slow_file)) {
  if (!slow_file_.empty()) {
    for (auto& n : read_list(slow_file_)) slow_.insert(std::move(n));
  }
  for (std::size_t i = 0; i < names_.size(); ++i) {
    if (usable(i)) {
      at_ = i;
      break;
    }
  }
}

bool Playlist::in_family(const std::string& name) const {
  if (family_.empty()) return true;
  for (const auto& f : family_)
    if (name.starts_with(f)) return true;
  return false;
}

bool Playlist::family_has_usable() const {
  for (const auto& n : names_)
    if (!slow_.contains(n) && !dropped_.contains(n) && in_family(n)) return true;
  return false;
}

bool Playlist::usable(std::size_t i) const {
  if (slow_.contains(names_[i]) || dropped_.contains(names_[i])) return false;
  return in_family(names_[i]) || !family_has_usable();
}

bool Playlist::has_other() const {
  for (std::size_t i = 0; i < names_.size(); ++i)
    if (i != at_ && usable(i)) return true;
  return false;
}

void Playlist::set_family(std::vector<std::string> prefixes) {
  family_ = std::move(prefixes);
  if (!names_.empty() && !usable(at_)) step(1);
}

bool Playlist::empty() const {
  for (std::size_t i = 0; i < names_.size(); ++i) {
    if (usable(i)) return false;
  }
  return true;
}

const std::string& Playlist::current() const {
  if (names_.empty() || !usable(at_)) return kNone;
  return names_[at_];
}

const std::string& Playlist::step(int dir) {
  const std::size_t n = names_.size();
  for (std::size_t k = 1; k <= n; ++k) {
    const std::size_t i = dir > 0 ? (at_ + k) % n : (at_ + n - k % n) % n;
    if (usable(i)) {
      at_ = i;
      return names_[i];
    }
  }
  return kNone;
}

bool Playlist::select(const std::string& name) {
  for (std::size_t i = 0; i < names_.size(); ++i) {
    if (names_[i] == name && usable(i)) {
      at_ = i;
      return true;
    }
  }
  return false;
}

const std::string& Playlist::next() { return step(1); }
const std::string& Playlist::prev() { return step(-1); }

const std::string& Playlist::shuffle(std::uint64_t seed) {
  std::vector<std::size_t> others;
  for (std::size_t i = 0; i < names_.size(); ++i) {
    if (usable(i) && i != at_) others.push_back(i);
  }
  if (others.empty()) return current();
  seed ^= seed >> 33U;
  seed *= 0xff51afd7ed558ccdULL;
  seed ^= seed >> 33U;
  at_ = others[seed % others.size()];
  return names_[at_];
}

void Playlist::mark_slow() {
  if (names_.empty() || !usable(at_)) return;
  const std::string name = names_[at_];
  slow_.insert(name);
  if (!slow_file_.empty()) std::ofstream(slow_file_, std::ios::app) << name << '\n';
  step(1);
}

void Playlist::drop() {
  if (names_.empty() || !usable(at_)) return;
  dropped_.insert(names_[at_]);
  step(1);
}

}  // namespace cadence::render::viz

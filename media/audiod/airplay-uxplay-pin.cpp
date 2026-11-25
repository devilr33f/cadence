#include "audiod/airplay-uxplay-pin.h"

#include "audiod/airplay-files.h"

#include <cstdio>
#include <fstream>
#include <algorithm>
#include <string>

namespace cadence::audiod::uxplay {

namespace {

const std::string kNone;

}  // namespace

bool plausibleKey(std::string_view key) {
  // 32 bytes in base64: 43 characters and one '=' of padding, or 44 without it
  if (key.size() != 44)
    return false;
  for (std::size_t i = 0; i < key.size(); ++i) {
    const char c = key[i];
    const bool alnum = (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9');
    if (alnum || c == '+' || c == '/' || (c == '=' && i == key.size() - 1))
      continue;
    return false;
  }
  return true;
}

PairedSenders::PairedSenders(std::string path) : path_(std::move(path)) {
  if (path_.empty())
    return;
  std::ifstream in(path_);
  std::string line;
  while (keys_.size() < kMaxPaired && std::getline(in, line))
    if (plausibleKey(line) && !known(line))
      keys_.push_back(line);
}

bool PairedSenders::known(std::string_view key) const {
  return std::find(keys_.begin(), keys_.end(), key) != keys_.end();
}

bool PairedSenders::add(std::string_view key) {
  if (!plausibleKey(key))
    return false;
  if (const auto found = std::find(keys_.begin(), keys_.end(), key); found != keys_.end())
    keys_.erase(found);
  keys_.emplace_back(key);
  if (keys_.size() > kMaxPaired)
    keys_.erase(keys_.begin());
  return save();
}

void PairedSenders::clear() {
  keys_.clear();
  save();
}

bool PairedSenders::save() const {
  if (path_.empty())
    return true;
  std::string text;
  for (const auto& key : keys_)
    text += key + "\n";
  const auto temporary = path_ + ".new";
  if (airplay::writePrivate(temporary, text))
    return false;
  return std::rename(temporary.c_str(), path_.c_str()) == 0;
}

bool PinPrompt::show(std::string_view pin, Clock::time_point now) {
  if (pin.size() != 4 || !std::all_of(pin.begin(), pin.end(), [](char c) { return c >= '0' && c <= '9'; }))
    return false;
  pin_ = std::string(pin);
  shownAt_ = now;
  return true;
}

bool PinPrompt::hide() {
  const bool was = pin_.has_value();
  pin_.reset();
  return was;
}

bool PinPrompt::expire(Clock::time_point now) {
  if (!pin_ || now - shownAt_ < kPinShown)
    return false;
  pin_.reset();
  return true;
}

const std::string& PinPrompt::pin() const { return pin_ ? *pin_ : kNone; }

std::optional<PinPrompt::Clock::time_point> PinPrompt::deadline() const {
  if (!pin_)
    return std::nullopt;
  return shownAt_ + kPinShown;
}

}  // namespace cadence::audiod::uxplay

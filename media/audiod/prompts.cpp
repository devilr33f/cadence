#include "audiod/prompts.h"

#include "audiod/sys.h"

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cerrno>
#include <cstddef>
#include <string>
#include <utility>

namespace cadence::audiod {

namespace {

bool allOf(std::string_view s, bool underscore) {
  for (const char c : s) {
    if (!((c >= 'a' && c <= 'z') || (underscore && c == '_')))
      return false;
  }
  return true;
}

}  // namespace

bool Prompts::validId(std::string_view id) { return !id.empty() && id.size() <= 32 && allOf(id, true); }

bool Prompts::validLang(std::string_view lang) { return lang.size() >= 2 && lang.size() <= 3 && allOf(lang, false); }

void Prompts::setDir(std::string_view dir) {
  if (dir == dir_)
    return;
  dir_ = std::string(dir);
  cache_.clear();
}

Prompts::Loaded Prompts::get(std::string_view lang, std::string_view id) {
  if (!validId(id) || !validLang(lang))
    return {.pcm = nullptr, .reason = "invalid"};
  if (dir_.empty())
    return {.pcm = nullptr, .reason = "unconfigured"};
  std::string key = std::string(lang).append("/").append(id);
  auto it = cache_.find(key);
  if (it == cache_.end())
    it = cache_.emplace(std::move(key), load(lang, id)).first;
  const auto& entry = it->second;
  if (entry.pcm)
    return {.pcm = &*entry.pcm, .reason = {}};
  return {.pcm = nullptr, .reason = entry.reason};
}

Prompts::Entry Prompts::load(std::string_view lang, std::string_view id) const {
  const std::string path = std::string(dir_).append("/").append(lang).append("/").append(id).append(".opus");
  UniqueFd fd(::open(path.c_str(), O_RDONLY | O_CLOEXEC | O_NOFOLLOW | O_NONBLOCK));
  if (!fd)
    return {.pcm = std::nullopt, .reason = errno == ENOENT ? "missing" : "read"};
  struct stat st {};
  if (::fstat(fd.get(), &st) != 0 || !S_ISREG(st.st_mode))
    return {.pcm = std::nullopt, .reason = "read"};
  if (st.st_size < 0 || static_cast<std::size_t>(st.st_size) > kMaxPromptFileBytes)
    return {.pcm = std::nullopt, .reason = std::string(promptErrorName(PromptErrorCode::TooLarge))};
  std::vector<std::uint8_t> bytes(static_cast<std::size_t>(st.st_size));
  std::size_t got = 0;
  while (got < bytes.size()) {
    const auto n = ::read(fd.get(), bytes.data() + got, bytes.size() - got);
    if (n < 0 && errno == EINTR)
      continue;
    if (n <= 0)
      return {.pcm = std::nullopt, .reason = "read"};
    got += static_cast<std::size_t>(n);
  }
  auto decoded = decodeOggOpus(bytes);
  if (!decoded.ok())
    return {.pcm = std::nullopt, .reason = std::string(promptErrorName(decoded.error()))};
  return {.pcm = std::move(decoded.value()), .reason = {}};
}

}  // namespace cadence::audiod

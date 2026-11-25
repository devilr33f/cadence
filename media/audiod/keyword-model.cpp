#include "audiod/keyword-model.h"

#include "audiod/sys.h"
#include "kws/tflm-model.h"
#include "sync/sha256.h"

#include <fcntl.h>
#include <sys/stat.h>

#include <cerrno>
#include <string>
#include <utility>
#include <vector>

namespace cadence::audiod {

namespace {

enum class Read : std::uint8_t { Ok, Missing, TooBig };

// a regular file of at most kKeywordModelMaxBytes, whole. nonblocking, so a fifo put in its place cannot hold the
// loop at open
Read readModel(const char* path, std::vector<std::uint8_t>& out) {
  UniqueFd fd(::open(path, O_RDONLY | O_CLOEXEC | O_NONBLOCK));
  if (!fd)
    return Read::Missing;
  struct stat st {};
  if (::fstat(fd.get(), &st) != 0 || !S_ISREG(st.st_mode))
    return Read::Missing;
  if (st.st_size < 0 || static_cast<std::uint64_t>(st.st_size) > kKeywordModelMaxBytes)
    return Read::TooBig;
  const auto expected = static_cast<std::size_t>(st.st_size);
  // one byte past what fstat said: a file that grows while it is read is no stable model
  out.resize(expected + 1);
  std::size_t size = 0;
  while (size < out.size()) {
    const auto got = ::read(fd.get(), out.data() + size, out.size() - size);
    if (got < 0 && errno == EINTR)
      continue;
    if (got < 0)
      return Read::Missing;
    if (got == 0)
      break;
    size += static_cast<std::size_t>(got);
  }
  if (size > expected)
    return Read::Missing;
  out.resize(size);
  return Read::Ok;
}

std::string hex(const sync::Sha256Digest& digest) {
  constexpr std::string_view kDigits = "0123456789abcdef";
  std::string out;
  out.reserve(digest.size() * 2);
  for (const auto byte : digest) {
    out.push_back(kDigits[static_cast<std::size_t>(byte >> 4)]);
    out.push_back(kDigits[static_cast<std::size_t>(byte & 0x0f)]);
  }
  return out;
}

kws::SpotterPolicy policyOf(const cadence_audiod_v1_KeywordEngine& config) {
  auto policy = kws::SpotterPolicy::defaultCapsula();
  if (config.pos_threshold > 0)
    policy.posThreshold = config.pos_threshold;
  if (config.sp_threshold > 0)
    policy.spThreshold = config.sp_threshold;
  if (config.rearm_frames > 0)
    policy.rearmFrames = config.rearm_frames;
  if (config.consecutive_calls > 0)
    policy.consecutiveCalls = config.consecutive_calls;
  return policy;
}

}  // namespace

Result<KeywordModel, std::string_view> loadTflmKeyword(std::span<const std::uint8_t> bytes) {
  auto loaded = kws::TflmModel::load(bytes, kKeywordArenaCap);
  if (!loaded.ok())
    return loaded.error();
  const auto arena = loaded.value()->arenaUsed();
  return KeywordModel{.model = std::move(loaded.value()), .arenaBytes = arena};
}

Result<KeywordSetup, std::string_view> resolveKeyword(const cadence_audiod_v1_KeywordEngine& config,
                                                      const KeywordModelLoader& load) {
  if (std::string_view(config.kind) != kKeywordKind)
    return std::string_view("kind_unknown");
  const std::string_view allowed = config.model_sha256;
  if (allowed.empty())
    return std::string_view("model_unknown");
  if (config.model_path[0] == '\0')
    return std::string_view("model_missing");
  std::vector<std::uint8_t> bytes;
  switch (readModel(config.model_path, bytes)) {
    case Read::Missing:
      return std::string_view("model_missing");
    case Read::TooBig:
      return std::string_view("model_invalid");
    case Read::Ok:
      break;
  }
  if (hex(sync::sha256(bytes)) != allowed)
    return std::string_view("model_unknown");
  auto model = load(bytes);
  if (!model.ok())
    return model.error();
  return KeywordSetup{.model = std::move(model.value()), .policy = policyOf(config)};
}

}  // namespace cadence::audiod

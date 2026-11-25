#pragma once

#include "kws/model.h"
#include "kws/spotter.h"
#include "result.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <span>
#include <string_view>

namespace cadence::audiod {

// the keyword engine audiod runs, and the token its wakes carry
inline constexpr std::string_view kKeywordKind = "vk-kws";
// a model file past this is refused unread
inline constexpr std::size_t kKeywordModelMaxBytes = 4 * 1024 * 1024;
// the stock model keeps 814 kB of arena
inline constexpr std::size_t kKeywordArenaCap = 2 * 1024 * 1024;
// the 20 ms frames the spotter may lag behind the capture (500 ms; a call takes about 25 ms of each 80 on the a53):
// past them the oldest go
inline constexpr std::size_t kKeywordQueueFrames = 25;

struct KeywordModel {
  std::unique_ptr<kws::KwsModel> model;
  // what the model keeps of its arena, for kws_started
  std::size_t arenaBytes = 0;
};

// builds the model from the bytes of a file whose sha256 matched. the error is a token: model_invalid, model_shape or
// arena_exceeded
using KeywordModelLoader = std::function<Result<KeywordModel, std::string_view>(std::span<const std::uint8_t> bytes)>;

// the tflm runner, its arena capped at kKeywordArenaCap
Result<KeywordModel, std::string_view> loadTflmKeyword(std::span<const std::uint8_t> bytes);

// what the keyword engine starts with
struct KeywordSetup {
  KeywordModel model;
  kws::SpotterPolicy policy;
};

// the Configure's keyword section against the device: kind_unknown (another engine than kKeywordKind), model_unknown
// (no sha256 named, or the file's is another), model_missing (no regular file to read at model_path), model_invalid
// (past kKeywordModelMaxBytes), else what load answers. tflm does not bounds-check a crafted model, so the file's
// bytes reach load only once their sha256 is the one the board allowlists. thresholds left at 0 take
// kws::SpotterPolicy::defaultCapsula()'s
Result<KeywordSetup, std::string_view> resolveKeyword(const cadence_audiod_v1_KeywordEngine& config,
                                                      const KeywordModelLoader& load);

}  // namespace cadence::audiod

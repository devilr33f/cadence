#include "audiod/airplay-uxplay-source.h"

namespace cadence::audiod::uxplay {

std::string_view Source::pause() {
  const auto reason = std::move(aside_);
  aside_.clear();
  if (reason == "displaced")
    session_->release("displaced");
  else
    session_->hold(true);
  return kMediaOk;
}

std::string_view Source::resume() {
  session_->hold(false);
  return kMediaOk;
}

std::string_view Source::stop() {
  aside_.clear();
  session_->release("stopped");
  return kMediaOk;
}

}  // namespace cadence::audiod::uxplay

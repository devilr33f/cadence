#include "audiod/airplay-uxplay-sync.h"

#include "dsp/format.h"

#include <algorithm>
#include <cmath>

namespace cadence::audiod::uxplay {

namespace {

// spans are bounded so ns times a rate (at most 192 kHz) stays within 64 bits
constexpr std::int64_t kMaxSpanNs = 3600LL * 1'000'000'000;

}  // namespace

std::size_t framesIn(std::int64_t ns, std::uint32_t rate) {
  if (ns <= 0)
    return 0;
  ns = std::min(ns, kMaxSpanNs);
  return static_cast<std::size_t>(ns * static_cast<std::int64_t>(rate) / 1'000'000'000);
}

std::uint64_t frameAt(std::int64_t monoNs, std::uint64_t frames, std::int64_t heardNs) {
  const auto span = std::clamp<std::int64_t>(monoNs - heardNs, -kMaxSpanNs, kMaxSpanNs);
  const auto offset = span * static_cast<std::int64_t>(dsp::kRate) / 1'000'000'000;
  const auto frame = static_cast<std::int64_t>(frames) + offset;
  return frame < 0 ? 0 : static_cast<std::uint64_t>(frame);
}

void Placer::reset() {
  started_ = false;
  steerPpm_ = 0;
  errorNs_ = 0;
  lateRun_ = 0;
  owed_ = 0;
  restartWindow();
}

void Placer::restartWindow() {
  windowStartNs_ = 0;
  windowMinNs_ = 0;
  recoverable_ = false;
}

void Placer::arrived(std::int64_t targetNs, std::int64_t readyNs) {
  const auto margin = targetNs + delayNs_ - readyNs;
  if (windowStartNs_ == 0) {
    windowStartNs_ = readyNs;
    windowMinNs_ = margin;
    return;
  }
  windowMinNs_ = std::min(windowMinNs_, margin);
  if (readyNs - windowStartNs_ < kRecoverWindowNs)
    return;
  recoverableNs_ = windowMinNs_;
  recoverable_ = true;
  windowStartNs_ = readyNs;
  windowMinNs_ = margin;
}

void Placer::delayTo(std::int64_t delayNs, std::int64_t targetNs, std::int64_t heardNs, std::uint32_t rate,
                     Placement& out) {
  const auto capped = std::clamp<std::int64_t>(delayNs, 0, kMaxDelayNs);
  if (capped > delayNs_) {
    delayNs_ = capped;
    out.shifted = true;
    // margins measured against the old delay are not the new one's
    restartWindow();
  }
  if (const auto rest = heardNs - (targetNs + delayNs_); rest > 0) {
    owed_ += framesIn(rest, rate);
    out.caughtUpNs = rest;
  }
}

Placement Placer::place(std::int64_t targetNs, std::int64_t heardNs, std::size_t frames, std::uint32_t rate) {
  Placement out;
  if (!started_) {
    started_ = true;
    errorNs_ = 0;
    lateRun_ = 0;
    owed_ = 0;
    // a stream that cannot be heard on time is delayed once, by what it lacks, rather than cut
    const auto late = heardNs - (targetNs + delayNs_);
    if (late > kShiftNs || (late > 0 && delayNs_ > 0 && late > kDeadBandNs)) {
      delayTo(delayNs_ + late, targetNs, heardNs, rate, out);
    } else if (late < -kDeadBandNs && delayNs_ > 0) {
      // the output caught up (a shorter latency): give back what the delay no longer needs
      delayNs_ = std::max<std::int64_t>(0, delayNs_ + late);
      restartWindow();
    }
    const auto gap = targetNs + delayNs_ - heardNs;
    if (gap > 0)
      out.silence = framesIn(gap, rate);
    else
      owed_ = framesIn(-gap, rate);
  } else if (owed_ == 0 && recoverable_ && delayNs_ > 0 && recoverableNs_ > kSlackNs) {
    // every packet of a whole window came this much earlier than it had to: the delay gives it back, and the audio
    // queued for the longer delay is dropped from the next frames, so the stream is heard that much sooner
    const auto give = std::min(delayNs_, recoverableNs_ - kSlackNs);
    delayNs_ -= give;
    owed_ = framesIn(give, rate);
    out.recovered = true;
    ++corrections_;
    errorNs_ = 0;
    lateRun_ = 0;
    restartWindow();
  } else if (owed_ == 0) {
    const auto error = targetNs + delayNs_ - heardNs;
    // the output's latency jumped (a few packets in a row, never one estimate): follow it rather than drop a tenth of
    // a second or more, up to kMaxDelayNs; a later burst than that is dropped to catch up
    if (-error > kShiftNs) {
      if (++lateRun_ >= kShiftAfter) {
        delayTo(delayNs_ - error, targetNs, heardNs, rate, out);
        lateRun_ = 0;
        errorNs_ = 0;
        ++corrections_;
      }
    } else {
      lateRun_ = 0;
      errorNs_ += kErrorSmoothing * (static_cast<double>(error) - errorNs_);
      if (std::abs(errorNs_) > static_cast<double>(kHardNs)) {
        // more than steering takes up in good time: cut, once, and steer on from there
        ++corrections_;
        out.hard = true;
        if (errorNs_ > 0)
          // early: audio was lost, or the output caught up
          out.silence = framesIn(static_cast<std::int64_t>(errorNs_), rate);
        else
          // late: what is owed is dropped from this packet and the next
          owed_ = framesIn(static_cast<std::int64_t>(-errorNs_), rate);
        errorNs_ = 0;
      } else {
        // early (positive) plays slower, late faster
        const auto ppm = -errorNs_ * static_cast<double>(kSteerPpmPerMs) / 1'000'000.0;
        steerPpm_ = static_cast<std::int32_t>(
            std::clamp(ppm, -static_cast<double>(kMaxSteerPpm), static_cast<double>(kMaxSteerPpm)));
      }
    }
  }
  out.steerPpm = steerPpm_;
  out.drop = std::min(frames, owed_);
  owed_ -= out.drop;
  return out;
}

}  // namespace cadence::audiod::uxplay

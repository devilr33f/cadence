#include "sync/group-playout.h"

#include "sync/calibration/probe.h"

#include <algorithm>
#include <cmath>


namespace cadence::sync {

namespace {

constexpr std::size_t kFrameValues = kGroupFrameSamples * kGroupChannels;
// frames queued at most (a minute): what a stalled playout keeps of what the stream sends
constexpr std::size_t kMaxQueued = 3000;
// lost frames in a row it conceals before it rejoins: half a second
constexpr std::uint64_t kMaxConcealed = 25;
// how far past the output's latency the input is kept fed with silence while the first frame is not due yet: by the
// playhead, not by what the input holds, so a mixer that overslept and catches up in a burst reads silence, never an
// empty input
constexpr std::int64_t kKeepFedNs = 20'000'000;
// what the input holds at most of that silence: a playhead that stops moving (a mixer that stopped) asks for more
// and more
constexpr std::int64_t kMaxKeptNs = 250'000'000;
// how far past the output's latency and one of its periods what was pushed reaches when a playout with nothing more
// lets it fade: past the splicer's and the resampler's hold (12 ms) and a pump's slack. the period counts whole, as the
// next read takes one: a tv's 43 ms read the input short past a fixed 30 ms, an underrun at every track's end
constexpr std::int64_t kRunOutNs = 30'000'000;
// a live trim moves at 0.1 % (1.7 cents, under what an ear hears on a held note; 1 % is 17 cents, a heard detune),
// so a correction of a few ms takes seconds. past 50 ms that would be a minute of drift: one clean jump is better
constexpr std::int32_t kTrimSlewPpm = 1'000;
constexpr std::int64_t kTrimJumpNs = 50'000'000;

std::int64_t framesNs(std::uint64_t frames) { return static_cast<std::int64_t>(frames * 1'000'000 / 48); }

std::int64_t nsFrames(std::int64_t ns) {
  return static_cast<std::int64_t>(std::llround(static_cast<double>(ns) * 48.0 / 1'000'000.0));
}

}  // namespace

std::string_view rejoinToken(RejoinReason reason) {
  switch (reason) {
    case RejoinReason::None:
      return "none";
    case RejoinReason::Error:
      return "placement";
    case RejoinReason::Underrun:
      return "underrun";
    case RejoinReason::ClockLost:
      return "clock_lost";
    case RejoinReason::ClockStep:
      return "clock_step";
    case RejoinReason::Gap:
      return "gap";
  }
  return "none";
}

GroupPlayout::GroupPlayout(PcmInput& out, PlayheadSource playhead, GroupPlayoutConfig config)
    : out_(out),
      playhead_(std::move(playhead)),
      config_(config),
      splicer_(kGroupChannels, config.fadeFrames),
      placer_(config.placer),
      pcm_(kFrameValues),
      zeros_(kFrameValues, 0.0f),
      appliedTrimNs_(config.trimNs) {
  placed_.reserve(kPlacedKept);
}

GroupPlayout::~GroupPlayout() = default;

void GroupPlayout::format(std::uint32_t epoch, MusicCodec codec) {
  std::lock_guard lock(mutex_);
  if (epoch == epoch_ && codec == codec_ && decoder_)
    return;
  if (epoch != epoch_) {
    // the old epoch's audio, if it still plays, stops for the new one
    if (started_)
      flushed_ = true;
    pauseAtNs_.reset();
    if (stats_.state == PlayoutState::Paused)
      stats_.state = PlayoutState::Buffering;
  }
  epoch_ = epoch;
  codec_ = codec;
  codecChanged_ = true;
  std::erase_if(queue_, [epoch](const auto& entry) { return entry.first.first < epoch; });
  std::erase_if(ultrasonic_, [epoch](const auto& slot) { return slot.epoch < epoch; });
}

void GroupPlayout::frame(std::uint32_t epoch, std::uint64_t seq, std::int64_t tNs, std::uint64_t mediaMs,
                         std::vector<std::uint8_t> data) {
  std::lock_guard lock(mutex_);
  if (epoch < epoch_)
    return;
  if (pauseAtNs_ && epoch == epoch_ && tNs >= *pauseAtNs_)
    return;
  queue_.insert_or_assign(Key{epoch, seq}, Queued{tNs, mediaMs, std::move(data)});
  while (queue_.size() > kMaxQueued)
    queue_.erase(queue_.begin());
}

void GroupPlayout::pause(std::uint32_t epoch, std::int64_t tNs) {
  std::lock_guard lock(mutex_);
  if (epoch != epoch_)
    return;
  pauseAtNs_ = tNs;
  std::erase_if(queue_, [&](const auto& entry) { return entry.second.tNs >= tNs; });
}

void GroupPlayout::flush(std::uint32_t epoch) {
  std::lock_guard lock(mutex_);
  std::erase_if(queue_, [epoch](const auto& entry) { return entry.first.first < epoch; });
  ultrasonic_.clear();
  flushed_ = true;
  pauseAtNs_.reset();
  if (stats_.state == PlayoutState::Paused)
    stats_.state = PlayoutState::Idle;
}

void GroupPlayout::rejoinFrom(std::int64_t fromNs) {
  std::lock_guard lock(mutex_);
  fromNs_ = fromNs;
  queue_.clear();
}

GroupTrimMode GroupPlayout::setTrim(std::int64_t ns) {
  std::lock_guard lock(mutex_);
  const auto from = config_.trimNs;
  config_.trimNs = ns;
  if (!started_ || stats_.state != PlayoutState::Playing || std::llabs(ns - from) > kTrimJumpNs) {
    appliedTrimNs_ = ns;
    trimSlewPpm_ = 0;
    trimAtMonoNs_ = 0;
    return GroupTrimMode::Jump;
  }
  // the next pump moves from what is actually in force, including when a new target replaces a slew in flight
  trimAtMonoNs_ = lastPumpMono_;
  return GroupTrimMode::Slew;
}

std::int64_t GroupPlayout::trimNs() const {
  std::lock_guard lock(mutex_);
  return config_.trimNs;
}

void GroupPlayout::setChannel(GroupChannelMap channel) {
  std::lock_guard lock(mutex_);
  config_.channel = channel;
}

void GroupPlayout::mixUltrasonic(std::uint32_t epoch, std::int64_t tNs, std::uint32_t code) {
  // one table per code, kept: the pump reads it under the lock, so it is built before taking it
  static std::mutex tablesMutex;
  static std::map<std::uint32_t, std::shared_ptr<const calibration::ProbeTable>> tables;
  std::shared_ptr<const calibration::ProbeTable> table;
  {
    std::lock_guard guard(tablesMutex);
    auto& cached = tables[code];
    if (!cached)
      cached = std::make_shared<const calibration::ProbeTable>(calibration::ultrasonicProbe(), code);
    table = cached;
  }
  std::lock_guard lock(mutex_);
  ultrasonic_.push_back({epoch, tNs, code, std::move(table)});
}

void GroupPlayout::clearUltrasonic() {
  std::lock_guard lock(mutex_);
  ultrasonic_.clear();
}

std::uint32_t GroupPlayout::epoch() const {
  std::lock_guard lock(mutex_);
  return epoch_;
}

PlayoutStats GroupPlayout::stats() const {
  std::lock_guard lock(mutex_);
  auto s = stats_;
  s.steerPpm = placer_.steerPpm();
  s.errorNs = placer_.smoothedErrorNs();
  s.cuts = placer_.cuts();
  s.queuedNs = static_cast<std::int64_t>(queue_.size()) * kGroupFrameNs;
  return s;
}

PlayoutDrop GroupPlayout::lastDrop() const {
  std::lock_guard lock(mutex_);
  return lastDrop_;
}

std::vector<PlacedFrame> GroupPlayout::placed() const {
  std::lock_guard lock(mutex_);
  return placed_;
}

std::optional<PlacedFrame> GroupPlayout::heardAt(std::int64_t nowMono) const {
  std::lock_guard lock(mutex_);
  // what was placed before a pause or a run-out is heard out after the playout stopped placing: started or not, the
  // last frame due by now is the one heard (a cleared input took the ones never heard)
  for (auto it = placed_.rbegin(); it != placed_.rend(); ++it)
    if (it->dueMonoNs <= nowMono)
      return *it;
  return std::nullopt;
}

void GroupPlayout::probe(const Playhead& playhead, const ClockMapping& mapping) {
  if (!config_.heard || config_.heardEvery == 0 || playhead.periodFrames == 0)
    return;
  // the formula below holds while every period after the frame's read a whole period: one that came up short, or a
  // clear's skip, leaves what was read up to it untold
  const bool whole = probeSeen_ && playhead.frames >= probeSeen_->frames &&
                     playhead.musicRead - probeSeen_->musicRead == playhead.frames - probeSeen_->frames;
  probeSeen_ = playhead;
  if (!whole)
    probeWholeFrom_ = playhead.musicRead;
  // placed_ runs in input positions, which only grow (a clear moves the reader up, never back)
  for (const auto& f : placed_) {
    if (f.seq % config_.heardEvery != 0 || (probedAny_ && Key{f.epoch, f.seq} <= probed_))
      continue;
    if (f.position > playhead.musicRead)
      break;
    probed_ = Key{f.epoch, f.seq};
    probedAny_ = true;
    // read by the latest period, or one just before it: the playhead formula holds for it
    const auto behind = playhead.musicRead - f.position;
    if (behind > 4 * playhead.periodFrames || f.position < probeWholeFrom_)
      continue;
    const double frames = static_cast<double>(playhead.periodFrames) - static_cast<double>(behind);
    const auto mono = playhead.heardNs + static_cast<std::int64_t>(std::llround(frames * 1e9 / 48000.0));
    config_.heard(f.epoch, f.seq, mapping.toLeader(config_.local.toSync(mono)));
  }
}

void GroupPlayout::record(const PlacedFrame& frame) {
  if (placed_.size() == kPlacedKept)
    placed_.erase(placed_.begin(), placed_.begin() + kPlacedKept / 2);
  placed_.push_back(frame);
}

std::optional<std::int64_t> GroupPlayout::heardNext(const Playhead& playhead) const {
  const auto status = out_.status();
  // a period that read the preroll: from then on the input keeps up and the formula is exact
  if (playhead.musicRead <= primedAt_ || status.writePos < playhead.musicRead)
    return std::nullopt;
  // the resampler's pending output to a fraction of a frame: whole frames alone would move the error in 21 us steps,
  // which the steering would chase as a limit cycle
  const auto whole = playhead.periodFrames + (status.writePos - playhead.musicRead) + splicer_.heldFrames();
  const double ahead = static_cast<double>(whole) + out_.pendingFrames();
  return playhead.heardNs + static_cast<std::int64_t>(std::llround(ahead * 1e9 / 48000.0));
}

std::int64_t GroupPlayout::dueOf(std::int64_t tNs, const ClockMapping& mapping) const {
  return config_.local.toMono(mapping.toLocal(tNs)) + appliedTrimNs_;
}

void GroupPlayout::decode(const Queued* queued, std::vector<float>& pcm) {
  if (queued == nullptr || !decoder_ || !decoder_->decode(queued->data, pcm)) {
    if (decoder_)
      decoder_->conceal(pcm);
    else
      std::fill(pcm.begin(), pcm.end(), 0.0f);
    ++stats_.concealed;
  }
  mapChannels(pcm);
}

void GroupPlayout::mapChannels(std::vector<float>& pcm) const {
  for (std::size_t i = 0; i + 1 < pcm.size(); i += 2) {
    switch (config_.channel) {
      case GroupChannelMap::Both:
        break;
      case GroupChannelMap::Left:
        pcm[i + 1] = pcm[i];
        break;
      case GroupChannelMap::Right:
        pcm[i] = pcm[i + 1];
        break;
      case GroupChannelMap::Mono:
        pcm[i] = pcm[i + 1] = 0.5f * (pcm[i] + pcm[i + 1]);
        break;
    }
  }
}

void GroupPlayout::push(std::span<const float> stereo) {
  if (!stereo.empty())
    out_.tryPush(stereo, kGroupRate, kGroupChannels);
}

void GroupPlayout::flushSplicer() {
  push(splicer_.output());
  splicer_.clearOutput();
}

void GroupPlayout::fadeOut() {
  splicer_.silence(0);
  flushSplicer();
  // the resampler holds its latency back: silence after the fade lets all of it out
  silence(out_.heldFrames());
}

void GroupPlayout::silence(std::size_t frames) {
  while (frames > 0) {
    const auto n = std::min(frames, kGroupFrameSamples);
    push(std::span<const float>(zeros_.data(), n * kGroupChannels));
    frames -= n;
  }
}

void GroupPlayout::advanceTrim(std::int64_t nowMono) {
  if (appliedTrimNs_ == config_.trimNs) {
    trimSlewPpm_ = 0;
    trimAtMonoNs_ = nowMono;
    return;
  }
  const auto elapsed = trimAtMonoNs_ == 0 ? 0 : std::max(nowMono - trimAtMonoNs_, std::int64_t{0});
  trimAtMonoNs_ = nowMono;
  // 1,000 ppm is 1 ms per second. the resampler takes the same rate (with the opposite sign), so the due-time move
  // changes where the continuous stream lands without a splice; the placer's own small-error steering rides on it
  const auto step = elapsed * kTrimSlewPpm / 1'000'000;
  const auto left = config_.trimNs - appliedTrimNs_;
  appliedTrimNs_ += std::clamp(left, -step, step);
  trimSlewPpm_ = appliedTrimNs_ == config_.trimNs ? 0 : left < 0 ? kTrimSlewPpm : -kTrimSlewPpm;
}

void GroupPlayout::reset(PlayoutState state, bool primed) {
  // the next start is a fresh placement, so any correction still slewing lands whole
  appliedTrimNs_ = config_.trimNs;
  trimSlewPpm_ = 0;
  trimAtMonoNs_ = 0;
  started_ = false;
  seen_.reset();
  primed_ = primed;
  splicer_.reset();
  placer_.reset();
  if (attached_)
    out_.detach();
  attached_ = false;
  stats_.state = state;
}

void GroupPlayout::clearInput() {
  // what the reader had not reached is never heard: its placements go, so neither the probe nor heardAt tells of them
  const auto read = out_.status().readPos;
  out_.clear();
  std::erase_if(placed_, [read](const PlacedFrame& f) { return f.position >= read; });
}

RejoinReason GroupPlayout::rejoin(RejoinReason reason, std::optional<std::int64_t> errorNs) {
  lastDrop_ = PlayoutDrop{.reason = reason,
                          .errorNs = errorNs.value_or(placer_.smoothedErrorNs()),
                          .delayFrames = stats_.delayFrames,
                          .delayMinFrames = delayMinFrames_,
                          .delayMaxFrames = delayMaxFrames_,
                          .queuedNs = static_cast<std::int64_t>(queue_.size()) * kGroupFrameNs,
                          .playedNs = started_ ? lastPumpMono_ - startedMono_ : 0};
  // what is queued in the input is placed wrong: it goes, and the next start fades in on time
  clearInput();
  reset(PlayoutState::Rejoining);
  queue_.clear();
  ++stats_.rejoins;
  return reason;
}

bool GroupPlayout::readShort(const Playhead& playhead) {
  // each period reads periodFrames of the input while it keeps up: a period that read less (an underrun the counter
  // did not see, as the input was not attached yet) moved what is pushed after it later than a playhead before it says
  bool shortRead = false;
  if (seen_ && playhead.frames > seen_->frames)
    shortRead = playhead.musicRead - seen_->musicRead < playhead.frames - seen_->frames;
  if (!seen_ || playhead.frames >= seen_->frames)
    seen_ = playhead;
  return shortRead;
}

RejoinReason GroupPlayout::starved(const Playhead& playhead, std::uint64_t underruns) {
  stats_.underruns += underruns;
  underrunsSeen_ = out_.status().underruns;
  if (playhead.musicRead < startedAt_) {
    // nothing it placed was heard yet (a start placed by a playhead the input ran dry after): the start goes, and the
    // next pump primes and starts again from the queue, which it keeps
    clearInput();
    reset(PlayoutState::Buffering);
    return RejoinReason::None;
  }
  return rejoin(RejoinReason::Underrun);
}

void GroupPlayout::release() {
  std::lock_guard lock(mutex_);
  ultrasonic_.clear();
  if (started_) {
    fadeOut();
  }
  reset(PlayoutState::Idle);
  out_.steer(0);
  out_.setAlwaysResample(false);
}

RejoinReason GroupPlayout::pump(std::int64_t nowMono, const std::optional<ClockMapping>& mapping) {
  std::lock_guard lock(mutex_);
  lastPumpMono_ = nowMono;
  advanceTrim(nowMono);
  if (flushed_) {
    flushed_ = false;
    if (started_ || primed_) {
      clearInput();
      reset(PlayoutState::Buffering);
    }
  }
  if (codecChanged_) {
    codecChanged_ = false;
    decoder_ = std::make_unique<MusicDecoder>(codec_);
  }
  if (epoch_ == 0 || !decoder_) {
    stats_.state = PlayoutState::Idle;
    return RejoinReason::None;
  }
  if (!mapping) {
    if (started_)
      return rejoin(RejoinReason::ClockLost);
    stats_.state = PlayoutState::Syncing;
    return RejoinReason::None;
  }
  if (haveGeneration_ && mapping->generation != generation_ && started_) {
    generation_ = mapping->generation;
    return rejoin(RejoinReason::ClockStep);
  }
  generation_ = mapping->generation;
  haveGeneration_ = true;
  if (stats_.state == PlayoutState::Paused)
    return RejoinReason::None;
  const auto playhead = playhead_ ? playhead_() : std::nullopt;
  if (!playhead) {
    // a playhead missing for a moment while it plays changes nothing of what was placed: only a start sets playing
    // again, so a playout that plays on stays so
    if (!started_)
      stats_.state = PlayoutState::Buffering;
    return RejoinReason::None;
  }
  probe(*playhead, *mapping);
  stats_.delayFrames = nsFrames(playhead->latencyNs);
  if (started_) {
    delayMinFrames_ = std::min(delayMinFrames_, stats_.delayFrames);
    delayMaxFrames_ = std::max(delayMaxFrames_, stats_.delayFrames);
  }
  if (!primed_) {
    out_.setAlwaysResample(true);
    clearInput();
    primedAt_ = out_.status().writePos;
    silence(static_cast<std::size_t>(nsFrames(config_.prerollNs)));
    primed_ = true;
    underrunsSeen_ = out_.status().underruns;
    if (stats_.state != PlayoutState::Rejoining)
      stats_.state = PlayoutState::Buffering;
    return RejoinReason::None;
  }
  auto heard = heardNext(*playhead);
  if (!heard)
    return RejoinReason::None;

  if (!started_) {
    // the first frame it can start with: at or after a rejoin's from, and not due before what is pushed now is heard
    const Queued* first = nullptr;
    std::uint64_t firstSeq = 0;
    for (auto it = queue_.begin(); it != queue_.end();) {
      if (it->first.first != epoch_ || (fromNs_ && it->second.tNs < *fromNs_) ||
          dueOf(it->second.tNs, *mapping) < *heard) {
        it = queue_.erase(it);
        continue;
      }
      first = &it->second;
      firstSeq = it->first.second;
      break;
    }
    if (pauseAtNs_ && first == nullptr) {
      // the pause overtook a start or a rejoin: nothing of the epoch is left before it, so it is paused, not
      // rejoining until the next epoch
      stats_.state = PlayoutState::Paused;
      return RejoinReason::None;
    }
    if (first == nullptr || dueOf(first->tNs, *mapping) - *heard > config_.leadNs) {
      // nothing due yet: the input keeps reading silence, so the formula keeps holding
      const auto missing = nowMono + playhead->latencyNs + kKeepFedNs - *heard;
      const auto room = kMaxKeptNs - framesNs(out_.status().queued);
      if (missing > 0 && room > 0)
        silence(static_cast<std::size_t>(nsFrames(std::min(missing, room))));
      return RejoinReason::None;
    }
    const auto gap = nsFrames(dueOf(first->tNs, *mapping) - *heard);
    silence(static_cast<std::size_t>(std::max<std::int64_t>(gap, 0)));
    splicer_.reset();
    placer_.reset();
    started_ = true;
    startedMono_ = nowMono;
    delayMinFrames_ = delayMaxFrames_ = stats_.delayFrames;
    seen_ = *playhead;
    startedAt_ = out_.status().writePos + out_.heldFrames();
    playingEpoch_ = epoch_;
    nextSeq_ = firstSeq;
    fromNs_.reset();
    out_.attach();
    attached_ = true;
    underrunsSeen_ = out_.status().underruns;
    stats_.state = PlayoutState::Playing;
  }

  if (const auto underruns = out_.status().underruns; underruns != underrunsSeen_)
    return starved(*playhead, underruns - underrunsSeen_);
  if (readShort(*playhead))
    return starved(*playhead, 1);

  std::int64_t lastT = 0;
  bool haveLast = false;
  std::uint64_t concealedRun = 0;
  for (;;) {
    // each frame by the latest playhead: one taken before the thread stalled is stale by however long that was
    const auto latest = playhead_();
    if (!latest)
      break;
    if (readShort(*latest))
      return starved(*latest, 1);
    heard = heardNext(*latest);
    if (!heard)
      break;
    const auto found = queue_.find(Key{epoch_, nextSeq_});
    const Queued* queued = found == queue_.end() ? nullptr : &found->second;
    std::int64_t tNs = 0;
    if (queued != nullptr) {
      tNs = queued->tNs;
    } else if (haveLast) {
      tNs = lastT + kGroupFrameNs;
    } else if (!placed_.empty() && placed_.back().epoch == epoch_ && placed_.back().seq + 1 == nextSeq_) {
      tNs = placed_.back().tNs + kGroupFrameNs;
    } else {
      break;
    }
    // what is queued in the input keeps the lead ahead of real time, no more
    if (*heard - nowMono > latest->latencyNs + config_.leadNs)
      break;
    const auto due = dueOf(tNs, *mapping);
    if (pauseAtNs_ && tNs >= *pauseAtNs_) {
      // the pause fell on a frame boundary: what came before fades out over the splicer's hold
      fadeOut();
      reset(PlayoutState::Paused, true);
      return RejoinReason::None;
    }
    if (queued == nullptr) {
      const bool later = queue_.upper_bound(Key{epoch_, nextSeq_}) != queue_.end();
      if (!later) {
        // nothing after it: the stream ended or stalls. while the input holds enough it waits; once it is about to
        // run out the audio fades out and the next frame starts afresh, on time, instead of a rejoin
        if (*heard - nowMono > latest->latencyNs + framesNs(latest->periodFrames) + kRunOutNs)
          break;
        fadeOut();
        reset(PlayoutState::Buffering, true);
        return RejoinReason::None;
      }
      // a lost frame with others after it is concealed; a run of them is a gap the stream has to fill
      if (++concealedRun > kMaxConcealed)
        return rejoin(RejoinReason::Gap);
    } else {
      concealedRun = 0;
    }
    decode(queued, pcm_);
    const auto mediaMs = queued != nullptr ? queued->mediaMs : 0;
    if (queued != nullptr)
      queue_.erase(found);
    const auto error = *heard - due;
    const auto placement = placer_.place(error, kGroupFrameNs);
    if (placement.action == GroupPlacement::Action::Rejoin)
      return rejoin(RejoinReason::Error, error);
    std::span<const float> samples(pcm_);
    if (pauseAtNs_ && tNs + kGroupFrameNs > *pauseAtNs_) {
      const auto keep = static_cast<std::size_t>(std::clamp<std::int64_t>(nsFrames(*pauseAtNs_ - tNs), 0,
                                                                          static_cast<std::int64_t>(kGroupFrameSamples)));
      samples = samples.first(keep * kGroupChannels);
    }
    const auto ultrasonicNs = static_cast<std::int64_t>(std::ceil(
        calibration::probeDuration(calibration::ultrasonicProbe()) * 1e9));
    std::erase_if(ultrasonic_, [&](const auto& slot) {
      return slot.epoch < epoch_ || tNs >= slot.tNs + ultrasonicNs;
    });
    for (const auto& slot : ultrasonic_) {
      if (slot.epoch != epoch_ || !slot.table || tNs + kGroupFrameNs <= slot.tNs)
        continue;
      for (std::size_t n = 0; n < samples.size() / kGroupChannels; ++n) {
        const double at = static_cast<double>(tNs - slot.tNs) / 1e9 + static_cast<double>(n) / kGroupRate;
        const auto probe = static_cast<float>(slot.table->at(at));
        pcm_[2 * n] += probe;
        pcm_[2 * n + 1] += probe;
      }
    }
    const auto base = out_.status().writePos + out_.heldFrames() + splicer_.heldFrames();
    PlacedFrame placed{epoch_, nextSeq_, tNs, mediaMs, due, base};
    if (placement.action == GroupPlacement::Action::Cut && placement.cutNs > 0) {
      const auto drop = static_cast<std::size_t>(nsFrames(placement.cutNs));
      if (drop < samples.size() / kGroupChannels)
        splicer_.drop(samples, drop);
      // the frame's start is gone: the next one is placed exactly
      placed.position = 0;
    } else {
      if (placement.action == GroupPlacement::Action::Cut) {
        const auto pad = static_cast<std::size_t>(nsFrames(-placement.cutNs));
        splicer_.silence(pad);
        placed.position = base + pad;
      } else {
        out_.steer(placement.steerPpm + trimSlewPpm_);
      }
      splicer_.append(samples);
    }
    flushSplicer();
    if (placed.position != 0)
      record(placed);
    lastT = tNs;
    haveLast = true;
    ++nextSeq_;
    if (pauseAtNs_ && tNs + kGroupFrameNs > *pauseAtNs_) {
      fadeOut();
      reset(PlayoutState::Paused, true);
      return RejoinReason::None;
    }
  }
  return RejoinReason::None;
}

}  // namespace cadence::sync

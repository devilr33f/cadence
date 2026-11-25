#include "audiod/airplay-uxplay-session.h"

#include "audiod/airplay-files.h"
#include "audiod/airplay-uxplay-metadata.h"
#include "audiod/airplay-volume.h"
#include "dsp/format.h"
#include "text.h"

#include <time.h>

#include <algorithm>
#include <cerrno>
#include <cstdio>
#include <utility>

namespace cadence::audiod::uxplay {

namespace {

// at most this much of the sender's audio waits to fall due: alac's lead is under 2 s, aac-eld's 170 ms
constexpr std::size_t kMaxPendingFrames = 4 * kStreamRate;
// without audio for this long, the clock is anchored by the video
constexpr std::int64_t kAudioAnchorFreshNs = 1'000'000'000;
// libairplay can wait about two seconds for a lost packet's resend. past that, an audio-only sender that stopped
// without its flush reaching us is paused, not left running on the screen; the next packet resumes it
constexpr std::int64_t kNoAudioPauseNs = 2'500'000'000;
// the output's latency until the clock knows it
constexpr std::int64_t kDefaultLatencyNs = 50'000'000;
// a sender let go (stopped, displaced) asks again at once, as an apple sender does when its connection drops: refused
// this long, so it ends its cast instead of mirroring on into a screen that no longer shows it
constexpr auto kLetGoRefusal = std::chrono::seconds(10);
// a gap in the rtp timeline up to this long is lost packets, filled with as much silence; a longer one is a new
// position, which the placer places
constexpr std::int64_t kMaxConcealFrames = kStreamRate;
// the steering is applied in steps of this many ppm, at most this often: each change rebuilds speexdsp's table
constexpr std::int32_t kSteerStepPpm = 10;
constexpr std::int64_t kSteerEveryNs = 100'000'000;
// the most decoded packets kept for reuse
constexpr std::size_t kSpareChunks = 64;

std::int64_t readClock(clockid_t id) {
  timespec now{};
  ::clock_gettime(id, &now);
  return static_cast<std::int64_t>(now.tv_sec) * 1'000'000'000 + now.tv_nsec;
}

std::int64_t framesNs(std::size_t frames, std::uint32_t rate) {
  return static_cast<std::int64_t>(frames) * 1'000'000'000 / static_cast<std::int64_t>(rate);
}

}  // namespace

SessionClocks systemClocks() {
  return SessionClocks{
      .monoNs = [] { return readClock(CLOCK_MONOTONIC); },
      .realNs = [] { return readClock(CLOCK_REALTIME); },
      .steady = [] { return std::chrono::steady_clock::now(); },
  };
}

Session::Session(const Log& log, PcmInput& music, SessionHooks hooks, SessionSettings settings, SessionClocks clocks)
    : log_(log),
      home_(music),
      hooks_(std::move(hooks)),
      settings_(std::move(settings)),
      clocks_(std::move(clocks)),
      paired_(settings_.scratchDir.empty() ? std::string() : settings_.scratchDir + "/paired") {}

Session::~Session() {
  std::scoped_lock lock(mutex_);
  attachLocked(false);
  if (outputOpen_)
    target_->clear();
  outputOpen_ = false;
  target_ = nullptr;
}

void Session::events(std::shared_ptr<MediaSourceEvents> events) {
  std::scoped_lock lock(mutex_);
  events_ = std::move(events);
}

void Session::configure(SessionSettings settings) {
  std::scoped_lock lock(mutex_);
  settings_ = std::move(settings);
  paired_ = PairedSenders(settings_.scratchDir.empty() ? std::string() : settings_.scratchDir + "/paired");
}

// --- the posts: each holds alive_ weakly, so one that lands after the session is dropped

void Session::report(Report kind) {
  std::shared_ptr<MediaSourceEvents> events;
  {
    std::scoped_lock lock(mutex_);
    events = events_;
  }
  if (!events)
    return;
  hooks_.post([events, kind] {
    switch (kind) {
      case Report::Playing:
        events->playing();
        break;
      case Report::Paused:
        events->paused();
        break;
      case Report::Ended:
        events->ended();
        break;
      case Report::Buffering:
        events->buffering();
        break;
    }
  });
}

void Session::reportMetadata(MediaMetadata metadata) {
  std::shared_ptr<MediaSourceEvents> events;
  {
    std::scoped_lock lock(mutex_);
    events = events_;
    // the sender's name rides on what it says of its track (cleared with the sender); memory-only
    metadata.sender = clientName_;
  }
  if (events)
    hooks_.post([events, metadata = std::move(metadata)] { events->metadata(metadata); });
}

void Session::announceSender() {
  MediaMetadata current;
  {
    std::scoped_lock lock(mutex_);
    if (clientName_.empty())
      return;
    current = tracker_.current();
    // only a cover on disk is named
    if (current.artworkId != writtenArtwork_)
      current.artworkId.clear();
  }
  hooks_.post([this, alive = std::weak_ptr<char>(alive_), metadata = std::move(current)]() mutable {
    if (alive.lock())
      reportMetadata(std::move(metadata));
  });
}

void Session::reportProgress(std::uint64_t positionMs, std::uint64_t durationMs) {
  std::shared_ptr<MediaSourceEvents> events;
  {
    std::scoped_lock lock(mutex_);
    events = events_;
  }
  if (events)
    hooks_.post([events, positionMs, durationMs] {
      events->duration(durationMs);
      events->position(positionMs);
    });
}

void Session::reportMirror(MirrorReport report) {
  if (!hooks_.mirrorState)
    return;
  hooks_.post([alive = std::weak_ptr<char>(alive_), hook = hooks_.mirrorState, report = std::move(report)] {
    if (alive.lock())
      hook(report);
  });
}

void Session::reportPin(PinReport report) {
  if (!hooks_.pin)
    return;
  hooks_.post([alive = std::weak_ptr<char>(alive_), hook = hooks_.pin, report = std::move(report)] {
    if (alive.lock())
      hook(report);
  });
}

// --- the loop's side

void Session::openOutput(PcmInput& music) {
  std::scoped_lock lock(mutex_);
  if (outputOpen_ && target_ == &music)
    return;
  if (outputOpen_)
    closeLocked();
  const auto& before = outLocked();
  outputOpen_ = true;
  target_ = &music;
  movedLocked(before);
  placer_.reset();
  splicer_.reset();
}

// no push is under way once it returns: every push happens with the lock held
void Session::closeOutput() {
  std::scoped_lock lock(mutex_);
  closeLocked();
}

void Session::closeLocked() {
  // a closed output still clears the music input, as it always did: nothing of the session's stays queued there
  attachLocked(false);
  clearMusicLocked();
  const auto& before = outLocked();
  outputOpen_ = false;
  target_ = nullptr;
  movedLocked(before);
  placer_.reset();
  splicer_.reset();
  steerApplied_ = 0;
}

void Session::movedLocked(const PcmInput& before) {
  if (&outLocked() != &before)
    musicSeen_ = outLocked().status();
}

void Session::hold(bool held) {
  bool resumed = false;
  {
    std::scoped_lock lock(mutex_);
    if (held_ == held)
      return;
    held_ = held;
    if (held) {
      attachLocked(false);
      if (outputOpen_)
        clearMusicLocked();
      dropPendingLocked();
    }
    placer_.reset();
    splicer_.reset();
    steerApplied_ = 0;
    resumed = !held && (streaming_ || mirroring_);
  }
  log_.info("airplay_held", {{"held", held ? "yes" : "no"}});
  // a resume with nothing streaming waits for the sender's next audio, which reports playing itself
  if (held || resumed)
    report(held ? Report::Paused : Report::Playing);
}

void Session::release(std::string_view reason) {
  {
    std::scoped_lock lock(mutex_);
    attachLocked(false);
    if (outputOpen_)
      clearMusicLocked();
    dropPendingLocked();
    splicer_.reset();
    endMirrorLocked(reason);
    // a mirror the same sender starts again is a new one, reported as such
    mirroring_ = false;
    held_ = false;
    if (!deviceId_.empty()) {
      letGo_ = deviceId_;
      letGoUntil_ = clocks_.steady() + kLetGoRefusal;
    }
  }
  log_.info("airplay_released", {{"reason", reason}});
  if (hooks_.disconnect)
    hooks_.disconnect();
}

void Session::shutdown(std::string_view reason) {
  std::scoped_lock lock(mutex_);
  endMirrorLocked(reason);
  if (prompt_.hide())
    reportPin(PinReport{});
  attachLocked(false);
  if (outputOpen_)
    clearMusicLocked();
  dropPendingLocked();
  splicer_.reset();
  streaming_ = false;
}

void Session::speakerLevel(std::uint8_t level) {
  std::scoped_lock lock(mutex_);
  guard_.setCurrent(std::min(level, kMaxSenderLevel));
}

void Session::outputLatency(std::int64_t ns) {
  std::scoped_lock lock(mutex_);
  latencyNs_ = ns;
}

void Session::playhead(const Playhead& playhead) {
  std::scoped_lock lock(mutex_);
  latencyNs_ = playhead.latencyNs;
  if (playhead.periodFrames > 0)
    playhead_ = playhead;
}

AudioStats Session::takeStats() {
  std::scoped_lock lock(mutex_);
  auto out = stats_;
  out.leadAvgNs = leadCount_ > 0 ? leadSumNs_ / static_cast<std::int64_t>(leadCount_) : 0;
  out.queueAvgNs = queueCount_ > 0 ? queueSumNs_ / static_cast<std::int64_t>(queueCount_) : 0;
  out.decode = decodeTimes_.take();
  out.steerPpm = placer_.steerPpm();
  out.errorNs = placer_.errorNs();
  out.delayNs = placer_.delayNs();
  out.streaming = streaming_;
  const auto music = outLocked().status();
  out.overruns = music.dropped - std::min(music.dropped, musicSeen_.dropped);
  out.underruns = music.underruns - std::min(music.underruns, musicSeen_.underruns);
  musicSeen_ = music;
  stats_ = AudioStats{};
  stats_.rtpPriority = out.rtpPriority;
  stats_.steerMinPpm = stats_.steerMaxPpm = out.steerPpm;
  leadSumNs_ = queueSumNs_ = 0;
  leadCount_ = queueCount_ = 0;
  return out;
}

void Session::tick() {
  std::optional<std::uint8_t> level;
  bool paused = false;
  {
    std::scoped_lock lock(mutex_);
    const auto nowNs = clocks_.monoNs();
    pumpLocked(nowNs);
    if (streaming_ && !mirroring_ && !held_ && lastArrivalNs_ != 0 && nowNs - lastArrivalNs_ >= kNoAudioPauseNs) {
      endStreamLocked();
      paused = true;
      // queue it before a packet on the rtp thread can resume: its playing report must follow this one
      if (events_)
        hooks_.post([events = events_] { events->paused(); });
    }
    if (prompt_.expire(clocks_.steady()))
      reportPin(PinReport{});
    level = coalescer_.poll(clocks_.steady());
  }
  if (paused)
    log_.info("airplay_pause_inferred", {{"without_audio_ms", kNoAudioPauseNs / 1'000'000}});
  if (level && hooks_.senderVolume)
    hooks_.post([alive = std::weak_ptr<char>(alive_), hook = hooks_.senderVolume, level = *level] {
      if (alive.lock())
        hook(level);
    });
}

std::optional<ClockAnchor> Session::anchor() const {
  std::scoped_lock lock(mutex_);
  if (!mirroring_)
    return std::nullopt;
  if (audioAnchor_ && clocks_.monoNs() - audioAnchorAtNs_ < kAudioAnchorFreshNs)
    return audioAnchor_;
  if (videoAnchor_)
    return ClockAnchor{videoAnchor_->mediaNs, videoAnchor_->monoNs + placer_.delayNs()};
  return audioAnchor_;
}

bool Session::mirroring() const {
  std::scoped_lock lock(mutex_);
  return mirroring_;
}

bool Session::streaming() const {
  std::scoped_lock lock(mutex_);
  return streaming_;
}

// --- with the lock held

std::int64_t Session::toMonoLocked(std::int64_t realNs) const {
  return realNs - clocks_.realNs() + clocks_.monoNs();
}

void Session::dropPendingLocked() {
  while (!pending_.empty()) {
    if (spare_.size() < kSpareChunks)
      spare_.push_back(std::move(pending_.front().samples));
    pending_.pop_front();
  }
  pendingFrames_ = 0;
}

void Session::clearMusicLocked() {
  // the reader skips to what was written by now: a playhead from before cannot say where a push is heard
  auto& music = outLocked();
  clearedAt_ = music.status().writePos;
  music.clear();
}

void Session::attachLocked(bool attached) {
  if (attached == attached_)
    return;
  attached_ = attached;
  // only while open: pumpLocked attaches only then, and closeLocked detaches before it lets go
  if (attached)
    target_->attach();
  else
    target_->detach();
}

std::int64_t Session::heardLocked(std::int64_t nowNs) const {
  const auto music = outLocked().status();
  const auto held = framesNs(splicer_.heldFrames(), kStreamRate);
  // never before what is queued could play out from now
  const auto floor = nowNs + framesNs(static_cast<std::size_t>(music.queued), static_cast<std::uint32_t>(dsp::kRate));
  // the playhead is a position in the music input: another target (a feed) is due as it would play out here
  if (placedLocked() && playhead_ && playhead_->musicRead >= clearedAt_ && music.writePos >= playhead_->musicRead) {
    // the period's first frame is heard at heardNs; the input frames it read end with it, and what was pushed since
    // follows them: exact however long ago the period ran, while the input keeps up
    const auto ahead = playhead_->periodFrames + (music.writePos - playhead_->musicRead);
    const auto heard =
        playhead_->heardNs + framesNs(static_cast<std::size_t>(ahead), static_cast<std::uint32_t>(dsp::kRate));
    return std::max(heard, floor) + held;
  }
  const auto latency = latencyNs_ > 0 ? latencyNs_ : kDefaultLatencyNs;
  return floor + latency + held;
}

void Session::steerLocked(std::int32_t ppm, std::int64_t nowNs) {
  ppm = ppm / kSteerStepPpm * kSteerStepPpm;
  stats_.steerMinPpm = std::min(stats_.steerMinPpm, ppm);
  stats_.steerMaxPpm = std::max(stats_.steerMaxPpm, ppm);
  if (ppm == steerApplied_ || (nowNs - steerAtNs_ < kSteerEveryNs && steerAtNs_ != 0))
    return;
  steerApplied_ = ppm;
  steerAtNs_ = nowNs;
  target_->steer(ppm);
}

void Session::flushSplicerLocked() {
  const auto out = splicer_.output();
  if (!out.empty())
    target_->tryPush(out, kStreamRate, kStreamChannels);
  splicer_.clearOutput();
}

void Session::pumpLocked(std::int64_t nowNs) {
  bool pushed = false;
  while (!pending_.empty()) {
    auto& chunk = pending_.front();
    const auto heard = heardLocked(nowNs);
    const auto gapNs = framesNs(chunk.gap, kStreamRate);
    if (chunk.targetNs - gapNs + placer_.delayNs() - heard > kLeadNs)
      break;
    const auto frames = chunk.samples.size() / kStreamChannels;
    if (outputOpen_ && !held_) {
      attachLocked(true);
      // a lost packet's gap is the chunk's own start: placed with it, so a gap is never also a sync error. a feed's
      // push is not placed: it goes whole, its gap as silence, as it falls due
      const bool place = placedLocked();
      const auto placed =
          place ? placer_.place(chunk.targetNs - gapNs, heard, chunk.gap + frames, kStreamRate) : Placement{};
      if (placed.shifted || placed.caughtUpNs > 0) {
        ++stats_.shifts;
        log_.info("airplay_sync_shifted", {{"delay_ms", placer_.delayNs() / 1'000'000},
                                           {"caught_up_ms", placed.caughtUpNs / 1'000'000}});
      }
      if (placed.recovered) {
        ++stats_.recoveries;
        log_.info("airplay_sync_recovered", {{"delay_ms", placer_.delayNs() / 1'000'000},
                                             {"dropped_ms", framesNs(placed.drop, kStreamRate) / 1'000'000}});
      }
      if (placed.hard)
        ++stats_.hardCuts;
      if (place)
        steerLocked(placed.steerPpm, nowNs);
      if (placed.silence > 0) {
        splicer_.silence(placed.silence);
        stats_.silenceFrames += placed.silence;
      }
      const auto dropGap = std::min(placed.drop, chunk.gap);
      const auto dropData = placed.drop - dropGap;
      if (chunk.gap > dropGap)
        splicer_.silence(chunk.gap - dropGap);
      const std::span<const float> samples(chunk.samples);
      if (dropData > 0)
        splicer_.drop(samples, dropData);
      else
        splicer_.append(samples);
      flushSplicerLocked();
      pushed = true;
      pushedFrames_ += frames - std::min(frames, dropData);
      droppedFrames_ += placed.drop;
      stats_.droppedFrames += placed.drop;
      const auto skipNs = framesNs(dropData, kStreamRate);
      audioAnchor_ = ClockAnchor{chunk.remoteNs + skipNs, chunk.targetNs + placer_.delayNs() + skipNs};
      audioAnchorAtNs_ = nowNs;
    }
    pendingFrames_ -= frames;
    if (spare_.size() < kSpareChunks)
      spare_.push_back(std::move(chunk.samples));
    pending_.pop_front();
  }
  if (pushed) {
    const auto queued =
        framesNs(static_cast<std::size_t>(outLocked().status().queued), static_cast<std::uint32_t>(dsp::kRate));
    stats_.queueMinNs = queueCount_ == 0 ? queued : std::min(stats_.queueMinNs, queued);
    queueSumNs_ += queued;
    ++queueCount_;
  }
}

void Session::endStreamLocked() {
  attachLocked(false);
  if (outputOpen_)
    clearMusicLocked();
  dropPendingLocked();
  placer_.reset();
  splicer_.reset();
  steerApplied_ = 0;
  haveRtp_ = false;
  lastArrivalNs_ = 0;
  if (decoder_)
    decoder_->reset();
  audioAnchor_.reset();
  streaming_ = false;
}

MirrorReport Session::activeReportLocked() const {
  MirrorReport report;
  report.sessionId = mirrorId_;
  report.active = true;
  report.h265 = h265_;
  report.width = width_;
  report.height = height_;
  report.clientName = clientName_;
  return report;
}

void Session::endMirrorLocked(std::string_view reason) {
  if (!mirrorForwarded_)
    return;
  mirrorForwarded_ = false;
  MirrorReport report;
  report.sessionId = mirrorId_;
  report.reason = std::string(reason);
  reportMirror(std::move(report));
}

void Session::writeArtworkLocked(MediaMetadata& metadata) {
  if (metadata.artworkId.empty() || metadata.artworkId == writtenArtwork_)
    return;
  if (settings_.scratchDir.empty()) {
    metadata.artworkId.clear();
    return;
  }
  const auto path = settings_.scratchDir + "/run/artwork";
  const auto temporary = path + ".new";
  std::optional<SysError> error = airplay::privateDir(settings_.scratchDir + "/run");
  if (!error)
    error = airplay::writePrivate(temporary, tracker_.artwork());
  if (!error && std::rename(temporary.c_str(), path.c_str()) != 0)
    error = SysError{"rename", errno};
  if (error) {
    log_.warn("airplay_artwork_failed", {{"call", error->call}, {"errno", error->code}});
    metadata.artworkId.clear();
    return;
  }
  writtenArtwork_ = metadata.artworkId;
  log_.info("airplay_artwork", {{"bytes", tracker_.artwork().size()}});
}

void Session::metadataItemLocked(const airplay::MetadataItem& item) {
  if (auto changed = tracker_.item(item)) {
    writeArtworkLocked(*changed);
    // posted without the lock: report takes it again
    hooks_.post([this, alive = std::weak_ptr<char>(alive_), metadata = std::move(*changed)]() mutable {
      if (alive.lock())
        reportMetadata(std::move(metadata));
    });
  }
}

// --- uxplay::Events

void Session::connectionOpened() {
  std::scoped_lock lock(mutex_);
  if (connections_++ == 0)
    guard_.connected();
}

void Session::connectionClosed() {
  bool ended = false;
  {
    std::scoped_lock lock(mutex_);
    if (connections_ > 0 && --connections_ > 0)
      return;
    ended = streaming_ || mirroring_;
    endStreamLocked();
    endMirrorLocked("sender");
    mirroring_ = false;
    videoAnchor_.reset();
    if (prompt_.hide())
      reportPin(PinReport{});
    if (auto cleared = tracker_.reset())
      hooks_.post([this, alive = std::weak_ptr<char>(alive_), metadata = std::move(*cleared)]() mutable {
        if (alive.lock())
          reportMetadata(std::move(metadata));
      });
    held_ = false;
    clientName_.clear();
    coalescer_.clearPending();
  }
  log_.info("airplay_sender_gone");
  if (ended)
    report(Report::Ended);
}

void Session::connectionReset(int reason) {
  bool ended = false;
  {
    std::scoped_lock lock(mutex_);
    ended = streaming_ || mirroring_;
    endStreamLocked();
    endMirrorLocked("failed");
    mirroring_ = false;
  }
  log_.warn("airplay_connection_reset", {{"reason", reason}});
  if (ended)
    report(Report::Ended);
}

void Session::client(std::string_view deviceId, std::string_view /*model*/, std::string_view name, bool& admit) {
  std::scoped_lock lock(mutex_);
  if (!deviceId.empty() && deviceId == letGo_ && clocks_.steady() < letGoUntil_) {
    // the device id is the sender's own: never logged
    admit = false;
    log_.info("airplay_sender_refused", {{"reason", "let_go"}});
    return;
  }
  admit = true;
  deviceId_ = std::string(deviceId);
  clientName_ = text::boundedUtf8(name, kMetadataTextBytes);
  if (prompt_.hide())
    reportPin(PinReport{});
}

void Session::audioFormat(AudioCodec codec, std::uint16_t framesPerPacket, bool usingScreen, bool /*isMedia*/) {
  std::scoped_lock lock(mutex_);
  codec_ = codec;
  framesPerPacket_ = framesPerPacket;
  decoder_ = makeDecoder(codec, framesPerPacket);
  decoderFailed_ = decoder_ == nullptr;
  endStreamLocked();
  log_.info("airplay_audio_format", {{"codec", codecToken(codec)},
                                     {"frames", static_cast<unsigned>(framesPerPacket)},
                                     {"screen", usingScreen ? "yes" : "no"},
                                     {"decoder", decoder_ ? "ok" : "none"}});
}

void Session::audio(const AudioPacket& packet) {
  // libairplay's rtp thread decodes and feeds the mixer: ahead of the ui, behind the playback thread
  thread_local const ThreadPriority rtpPriority = raiseThreadPriority(kNiceAudioFeed);
  bool started = false;
  {
    std::scoped_lock lock(mutex_);
    stats_.rtpPriority = rtpPriority;
    if (packet.codec != codec_ || (!decoder_ && !decoderFailed_)) {
      codec_ = packet.codec;
      decoder_ = makeDecoder(packet.codec, framesPerPacket_);
      decoderFailed_ = decoder_ == nullptr;
    }
    if (!decoder_ || packet.localNs == 0)
      return;
    decoded_.clear();
    const auto decodeStart = clocks_.steady();
    const bool ok = decoder_->decode(packet.data, decoded_);
    decodeTimes_.record(std::chrono::duration_cast<std::chrono::nanoseconds>(clocks_.steady() - decodeStart).count());
    if (!ok || decoded_.empty()) {
      if (decodeFailures_++ % 500 == 0)
        log_.warn("airplay_decode_failed", {{"codec", codecToken(codec_)}, {"count", decodeFailures_}});
      return;
    }
    const auto nowNs = clocks_.monoNs();
    ++stats_.packets;
    if (lastArrivalNs_ != 0)
      stats_.stallMaxNs = std::max(stats_.stallMaxNs, nowNs - lastArrivalNs_);
    lastArrivalNs_ = nowNs;
    auto frames = decoded_.size() / kStreamChannels;
    auto targetNs = toMonoLocked(packet.localNs);
    auto remoteNs = packet.remoteNs;
    // the rtp timeline says what was lost or sent twice, exactly: a gap is filled with silence (spliced), an overlap
    // dropped; a jump past a second is a new position, left to the placer
    std::size_t gap = 0;
    std::size_t skip = 0;
    if (haveRtp_) {
      const auto delta = static_cast<std::int32_t>(packet.rtpTime - nextRtp_);
      const auto seqDelta = static_cast<std::uint16_t>(packet.seq - lastSeq_);
      if (seqDelta > 1 && seqDelta < 1024)
        stats_.lost += seqDelta - 1U;
      if (delta > 0 && delta <= kMaxConcealFrames) {
        gap = static_cast<std::size_t>(delta);
        stats_.concealedFrames += gap;
      } else if (delta < 0 && static_cast<std::size_t>(-static_cast<std::int64_t>(delta)) < frames) {
        skip = static_cast<std::size_t>(-static_cast<std::int64_t>(delta));
        stats_.overlapFrames += skip;
      }
    }
    haveRtp_ = true;
    nextRtp_ = packet.rtpTime + static_cast<std::uint32_t>(frames);
    lastSeq_ = packet.seq;
    if (skip > 0) {
      const auto skipNs = framesNs(skip, kStreamRate);
      targetNs += skipNs;
      remoteNs += skipNs;
      frames -= skip;
    }
    // how early it came, for the delay to give back what bursts of late packets made it take
    const auto readyNs = outputOpen_ && !held_ ? heardLocked(nowNs) : nowNs + (latencyNs_ > 0 ? latencyNs_ : kDefaultLatencyNs);
    placer_.arrived(targetNs, readyNs);
    const auto leadNs = targetNs - framesNs(gap, kStreamRate) + placer_.delayNs() - readyNs;
    stats_.leadMinNs = leadCount_ == 0 ? leadNs : std::min(stats_.leadMinNs, leadNs);
    leadSumNs_ += leadNs;
    ++leadCount_;
    if (leadNs < 0)
      ++stats_.late;
    std::vector<float> samples;
    if (!spare_.empty()) {
      samples = std::move(spare_.back());
      spare_.pop_back();
    }
    samples.assign(decoded_.begin() + static_cast<std::ptrdiff_t>(skip * kStreamChannels), decoded_.end());
    pending_.push_back(Chunk{targetNs, remoteNs, gap, std::move(samples)});
    pendingFrames_ += frames;
    // a sender far ahead, or an output that stopped reading: the oldest goes
    while (pendingFrames_ > kMaxPendingFrames) {
      pendingFrames_ -= pending_.front().samples.size() / kStreamChannels;
      if (spare_.size() < kSpareChunks)
        spare_.push_back(std::move(pending_.front().samples));
      pending_.pop_front();
    }
    if (!streaming_) {
      streaming_ = true;
      started = !held_;
    }
    pumpLocked(nowNs);
  }
  if (started) {
    log_.info("airplay_streaming", {{"codec", codecToken(packet.codec)}});
    report(Report::Playing);
    announceSender();
  }
}

void Session::audioFlush() {
  bool paused = false;
  {
    std::scoped_lock lock(mutex_);
    paused = streaming_ && !mirroring_;
    endStreamLocked();
  }
  // a mirror session's audio flushes with the screen still up
  if (paused)
    report(Report::Paused);
}

void Session::volume(float db) {
  const auto level = airplay::toPercent(db);
  if (!level)
    return;
  std::optional<std::uint8_t> send;
  {
    std::scoped_lock lock(mutex_);
    if (guard_.incoming(*level) != Incoming::Request)
      return;
    send = coalescer_.push(*level, clocks_.steady());
  }
  if (send && hooks_.senderVolume)
    hooks_.post([alive = std::weak_ptr<char>(alive_), hook = hooks_.senderVolume, level = *send] {
      if (alive.lock())
        hook(level);
    });
}

// the sender's slider starts at the speaker's level: what it sends back unchanged is no request
double Session::clientVolume() {
  std::scoped_lock lock(mutex_);
  return airplay::toDb(guard_.current().value_or(kUnknownLevel));
}

void Session::metadata(std::span<const std::uint8_t> dmap) {
  std::scoped_lock lock(mutex_);
  for (const auto& item : dmapItems(dmap))
    metadataItemLocked(item);
}

void Session::cover(std::span<const std::uint8_t> image) {
  if (image.size() > airplay::kMaxMetadataItem)
    return;
  std::scoped_lock lock(mutex_);
  metadataItemLocked(coverItem(image));
}

void Session::progress(std::uint32_t start, std::uint32_t current, std::uint32_t end) {
  const auto parsed = airplay::progress(progressItem(start, current, end));
  if (parsed)
    reportProgress(parsed->positionMs, parsed->durationMs);
}

void Session::mirrorRunning(bool running) {
  bool started = false;
  bool ended = false;
  {
    std::scoped_lock lock(mutex_);
    if (running == mirroring_)
      return;
    mirroring_ = running;
    if (running) {
      if (!settings_.mirroring)
        return;
      mirrorId_ = "airplay-" + std::to_string(++mirrorSessions_);
      mirrorForwarded_ = true;
      videoAnchor_.reset();
      reportMirror(activeReportLocked());
      started = !held_;
    } else {
      endMirrorLocked("sender");
      videoAnchor_.reset();
      ended = !streaming_;
    }
  }
  log_.info("airplay_mirroring", {{"active", running ? "yes" : "no"}});
  // the sessions read the kind again with the state: screencast while it mirrors
  if (started) {
    report(Report::Buffering);
    report(Report::Playing);
  } else if (ended) {
    report(Report::Ended);
  }
}

bool Session::videoCodec(bool h265) {
  std::scoped_lock lock(mutex_);
  if (h265 && !settings_.h265)
    return false;
  h265_ = h265;
  return true;
}

void Session::videoSize(float width, float height) {
  std::scoped_lock lock(mutex_);
  width_ = width > 0 ? static_cast<std::uint32_t>(width) : 0;
  height_ = height > 0 ? static_cast<std::uint32_t>(height) : 0;
  // a size that arrives once mirroring runs goes out with a new MirrorState
  if (mirrorForwarded_)
    reportMirror(activeReportLocked());
}

void Session::video(const VideoPacket& packet) {
  std::string id;
  {
    std::scoped_lock lock(mutex_);
    if (!mirrorForwarded_)
      return;
    id = mirrorId_;
    if (packet.localNs != 0)
      videoAnchor_ = ClockAnchor{packet.remoteNs, toMonoLocked(packet.localNs)};
  }
  if (!hooks_.mirrorUnit)
    return;
  auto unit = splitAccessUnit(packet.data, packet.h265);
  hooks_.post([alive = std::weak_ptr<char>(alive_), hook = hooks_.mirrorUnit, id = std::move(id),
               unit = std::move(unit), pts = packet.remoteNs] {
    if (alive.lock())
      hook(id, unit, pts);
  });
}

void Session::videoFlush() {}

void Session::pin(std::string_view pin) {
  std::scoped_lock lock(mutex_);
  if (!prompt_.show(pin, clocks_.steady()))
    return;
  log_.info("airplay_pin_shown");
  PinReport report;
  report.active = true;
  report.pin = std::string(pin);
  report.clientName = clientName_;
  reportPin(std::move(report));
}

void Session::registered(std::string_view /*deviceId*/, std::string_view publicKey) {
  std::scoped_lock lock(mutex_);
  if (!settings_.requirePin)
    return;
  const bool saved = paired_.add(publicKey);
  log_.info("airplay_sender_paired", {{"saved", saved ? "yes" : "no"}, {"paired", paired_.size()}});
  if (prompt_.hide())
    reportPin(PinReport{});
}

bool Session::known(std::string_view publicKey) {
  std::scoped_lock lock(mutex_);
  return !settings_.requirePin || paired_.known(publicKey);
}

void Session::hlsPlay(std::string_view location, float startS) {
  {
    // a new cast: nothing is known of its playback until the screen reports it
    std::scoped_lock lock(hlsMutex_);
    hls_ = HlsInfo{};
  }
  std::string name;
  {
    std::scoped_lock lock(mutex_);
    name = clientName_;
  }
  if (hooks_.hls)
    hooks_.post([hook = hooks_.hls, request = HlsRequest{HlsRequest::Op::Play, std::string(location), startS, std::move(name)}] {
      hook(request);
    });
}

void Session::hlsScrub(float positionS) {
  if (hooks_.hls)
    hooks_.post([hook = hooks_.hls, positionS] { hook(HlsRequest{HlsRequest::Op::Scrub, {}, positionS, {}}); });
}

void Session::hlsRate(float rate) {
  if (hooks_.hls)
    hooks_.post([hook = hooks_.hls, rate] { hook(HlsRequest{HlsRequest::Op::Rate, {}, rate, {}}); });
}

void Session::hlsStop() {
  if (hooks_.hls)
    hooks_.post([hook = hooks_.hls] { hook(HlsRequest{HlsRequest::Op::Stop, {}, 0, {}}); });
}

HlsInfo Session::hlsInfo() {
  std::scoped_lock lock(hlsMutex_);
  return hls_;
}

void Session::playbackInfo(const HlsInfo& info) {
  std::scoped_lock lock(hlsMutex_);
  hls_ = info;
}

}  // namespace cadence::audiod::uxplay

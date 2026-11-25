#include "player/mirror-presenter.h"

#include "ipc/log.h"
#include "player/video-fit.h"

#include <time.h>

#include <algorithm>
#include <chrono>

namespace cadence::screen::player {

namespace {

std::int64_t monotonicNs() {
  timespec ts{};
  ::clock_gettime(CLOCK_MONOTONIC, &ts);
  return static_cast<std::int64_t>(ts.tv_sec) * 1'000'000'000 + ts.tv_nsec;
}

MirrorCodec codecOf(int startCodec, std::span<const std::uint8_t> config) {
  if (startCodec == 1) return MirrorCodec::H264;
  if (startCodec == 2) return MirrorCodec::H265;
  return codecOfConfig(config);
}

struct Act {
  std::int64_t handle;
  bool render;
  std::int64_t atNs;
};

}  // namespace

MirrorPresenter::MirrorPresenter(std::unique_ptr<MirrorDecoder> decoder, Clock now)
    : decoder_(std::move(decoder)), now_(now ? std::move(now) : Clock(monotonicNs)) {
  thread_ = std::thread([this] { run(); });
}

MirrorPresenter::~MirrorPresenter() {
  {
    std::lock_guard lock(mu_);
    stopping_ = true;
  }
  cv_.notify_all();
  if (thread_.joinable()) thread_.join();
}

void MirrorPresenter::frame(const std::string& sessionId, std::int64_t ptsNs, bool config, bool keyframe,
                            std::span<const std::uint8_t> data) {
  {
    std::lock_guard lock(mu_);
    counts_.frames++;
    if (queue_.size() >= kMaxQueued || queuedBytes_ + data.size() > kMaxQueuedBytes) {
      // the decoder fell behind: what is queued is stale, and the stream resumes at a keyframe (a config is kept,
      // as the frames after it need it)
      std::deque<Frame> configs;
      for (auto& f : queue_)
        if (f.config) configs.push_back(std::move(f));
      queue_.swap(configs);
      queuedBytes_ = 0;
      for (const auto& f : queue_) queuedBytes_ += f.data.size();
      schedule_.lost();
      counts_.overflows++;
      if (!config && !keyframe) return;
    }
    queue_.push_back(Frame{sessionId, ptsNs, config, keyframe, {data.begin(), data.end()}});
    queuedBytes_ += data.size();
  }
  cv_.notify_one();
}

void MirrorPresenter::clock(std::int64_t mediaNs, std::int64_t monoNs, std::int64_t latencyNs) {
  std::lock_guard lock(mu_);
  schedule_.clock(mediaNs, monoNs, latencyNs);
}

void MirrorPresenter::start(const std::string& sessionId, int codec, int width, int height) {
  std::lock_guard lock(mu_);
  schedule_.start(sessionId);
  startSession_ = sessionId;
  startCodec_ = codec;
  startWidth_ = width;
  startHeight_ = height;
}

void MirrorPresenter::stop(const std::string& sessionId) {
  {
    std::lock_guard lock(mu_);
    if (!schedule_.stop(sessionId)) return;
    queue_.clear();
    queuedBytes_ = 0;
    pending_.reset();
    closeRequested_ = true;
  }
  cv_.notify_one();
}

bool MirrorPresenter::active() const {
  std::lock_guard lock(mu_);
  return schedule_.active();
}

MirrorPresenter::Counts MirrorPresenter::counts() const {
  std::lock_guard lock(mu_);
  return counts_;
}

void MirrorPresenter::closeDecoder() {
  for (const auto& o : held_) decoder_->release(o.handle, false, 0);
  held_.clear();
  if (open_) decoder_->close();
  open_ = false;
  ptsBaseNs_ = -1;
}

MirrorPresenter::Stats MirrorPresenter::statsLocked() const {
  return Stats{counts_, static_cast<std::uint64_t>(held_.size()), schedule_.anchored(), lastAheadNs_};
}

void MirrorPresenter::log(const char* event, const Stats& st) {
  const auto& c = st.counts;
  CADENCE_LOG(Info, event, {{"frames", c.frames},
                            {"decoded", c.decoded},
                            {"rendered", c.rendered},
                            {"late", c.late},
                            {"unsynced", c.unsynced},
                            {"early", c.early},
                            {"held", st.held},
                            {"skipped", c.skipped},
                            {"dropped", c.dropped},
                            {"overflows", c.overflows},
                            {"refused", c.refused},
                            {"anchored", st.anchored},
                            {"ahead_ms", st.aheadNs / 1'000'000}});
}

void MirrorPresenter::run() {
  std::vector<MirrorDecoder::Output> fresh;
  std::vector<Act> acts;
  for (;;) {
    std::optional<Frame> next;
    MirrorSchedule::Input input = MirrorSchedule::Input::Skip;
    bool close = false;
    // the next unit waits for its time before the decoder (a group's lead, rule 7)
    bool waiting = false;
    int codec = 0;
    int width = 0;
    int height = 0;
    {
      std::unique_lock lock(mu_);
      // idle: no decoder and nothing queued, so nothing is due either
      cv_.wait_for(lock, std::chrono::milliseconds(open_ ? 0 : 50),
                   [this] { return stopping_ || closeRequested_ || pending_ || !queue_.empty(); });
      if (stopping_) break;
      close = closeRequested_;
      closeRequested_ = false;
      if (!close) {
        if (pending_) {
          next = std::move(pending_);
          pending_.reset();
          input = MirrorSchedule::Input::Decode;
        } else if (!queue_.empty() && open_ && !queue_.front().config &&
                   schedule_.waitBeforeDecode(queue_.front().ptsNs, now_()) > 0) {
          waiting = true;
        } else if (!queue_.empty()) {
          next = std::move(queue_.front());
          queue_.pop_front();
          queuedBytes_ -= next->data.size();
          input = schedule_.frame(next->session, next->config, next->keyframe, next->data);
          if (input == MirrorSchedule::Input::Skip) counts_.skipped++;
          if (input == MirrorSchedule::Input::Configure && next->session == startSession_) {
            codec = startCodec_;
            width = startWidth_;
            height = startHeight_;
          }
        }
      }
    }
    // the decoder's calls are made outside the lock, so the tap and the main thread never wait on them
    if (close) {
      if (open_) {
        std::unique_lock lock(mu_);
        const auto st = statsLocked();
        lock.unlock();
        log("mirror_session_ended", st);
      }
      closeDecoder();
      // the session's shape goes with it (the decoder sets it); the video plane is the whole display again
      videoLayout().setContent(VideoLayout::Source::Mirror, std::nullopt);
      continue;
    }
    if (next && input == MirrorSchedule::Input::Configure) {
      closeDecoder();
      const auto c = codecOf(codec, next->data);
      open_ = c != MirrorCodec::Unknown &&
              decoder_->configure(c, width > 0 ? width : 1920, height > 0 ? height : 1080, next->data);
      const char* name = c == MirrorCodec::H265 ? "h265" : c == MirrorCodec::H264 ? "h264" : "unknown";
      if (open_)
        CADENCE_LOG(Info, "mirror_decoder_configured", {{"codec", name}});
      else
        CADENCE_LOG(Warn, "mirror_decoder_refused", {{"codec", name}});
      firstOutputSeen_ = false;
      nextStatsNs_ = now_() + kStatsEveryNs;
      std::lock_guard counted(mu_);
      (open_ ? counts_.configs : counts_.refused)++;
    }
    bool stalled = false;
    if (next && input == MirrorSchedule::Input::Decode && open_) {
      // the decoder's timeline starts near 0 at its first unit: the sender's (ntp-epoch) pts is put back on output
      if (ptsBaseNs_ < 0) ptsBaseNs_ = next->ptsNs;
      const auto queued = decoder_->queue(next->data, next->ptsNs - ptsBaseNs_ + kDecoderPtsStartNs, next->keyframe);
      if (queued == MirrorDecoder::Queued::Full) {
        stalled = true;
        std::lock_guard kept(mu_);
        // still the next frame, unless a stop came meanwhile
        if (!closeRequested_) pending_ = std::move(next);
      } else if (queued == MirrorDecoder::Queued::Dropped) {
        std::lock_guard counted(mu_);
        counts_.dropped++;
      }
    }
    if (!open_) continue;
    bool busy;
    {
      std::lock_guard lock(mu_);
      busy = !stalled && (pending_ || (!queue_.empty() && !waiting));
    }
    fresh.clear();
    // with input waiting and room for it, a short look; else (idle, or the decoder full until an output goes) the
    // output wait paces the loop
    decoder_->outputs(fresh, busy ? 0 : 4'000'000);
    for (auto& o : fresh) o.ptsNs = o.ptsNs - kDecoderPtsStartNs + (ptsBaseNs_ < 0 ? 0 : ptsBaseNs_);
    acts.clear();
    std::optional<Stats> stats;
    struct First {
      bool anchored;
      std::int64_t aheadNs;
      bool unsynced;
    };
    std::optional<First> first;
    {
      std::lock_guard lock(mu_);
      counts_.decoded += fresh.size();
      held_.insert(held_.end(), fresh.begin(), fresh.end());
      const auto now = now_();
      using K = MirrorSchedule::Release::Kind;
      for (auto it = held_.begin(); it != held_.end();) {
        const auto r = schedule_.output(it->ptsNs, now);
        lastAheadNs_ = r.atNs - now;
        if (!firstOutputSeen_) {
          firstOutputSeen_ = true;
          first = First{schedule_.anchored(), lastAheadNs_, r.unsynced};
        }
        if (r.kind == K::Hold) {
          ++it;
          continue;
        }
        const bool render = r.kind != K::Drop;
        acts.push_back(Act{it->handle, render, r.kind == K::Render ? r.atNs : 0});
        (render ? counts_.rendered : counts_.late)++;
        if (r.unsynced) counts_.unsynced++;
        it = held_.erase(it);
      }
      // too many held: the oldest go to the surface now with their release time, so the decoder keeps buffers
      while (held_.size() > kMaxHeld) {
        const auto r = schedule_.output(held_.front().ptsNs, now);
        acts.push_back(Act{held_.front().handle, true, std::max(r.atNs, now)});
        counts_.rendered++;
        counts_.early++;
        held_.erase(held_.begin());
      }
      if (now >= nextStatsNs_) {
        nextStatsNs_ = now + kStatsEveryNs;
        stats = statsLocked();
      }
    }
    for (const auto& a : acts) decoder_->release(a.handle, a.render, a.atNs);
    if (first) {
      // how the clock places the session's first picture: the one line that tells a clock that never fits
      CADENCE_LOG(Info, "mirror_first_output",
                  {{"anchored", first->anchored}, {"ahead_ms", first->aheadNs / 1'000'000}, {"unsynced", first->unsynced}});
    }
    if (stats) log("mirror_stats", *stats);
  }
  std::unique_lock lock(mu_);
  const auto st = statsLocked();
  lock.unlock();
  log("mirror_presenter_stopped", st);
  closeDecoder();
  videoLayout().setContent(VideoLayout::Source::Mirror, std::nullopt);
}

}  // namespace cadence::screen::player

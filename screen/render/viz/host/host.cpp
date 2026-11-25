#include "host.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <utility>

namespace cadence::render::viz {

namespace {

std::int64_t mono_now_ns() {
  // steady_clock is CLOCK_MONOTONIC on linux and android, the tap's clock
  return std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

}  // namespace

Host::Host(HostConfig c, std::unique_ptr<SharedContext> context, LogFn log)
    : c_(std::move(c)), context_(std::move(context)), log_(log), renderer_(c_.renderer, log) {
  if (!log_) log_ = [](const std::string&, const std::string&, double) {};
}

Host::~Host() { stop(); }

bool Host::start() {
  if (running_ || !context_) return false;
  running_ = true;
  thread_ = std::thread([this] { run(); });
  return true;
}

void Host::stop() {
  if (!running_.exchange(false)) return;
  if (thread_.joinable()) thread_.join();
}

void Host::next() {
  std::lock_guard lock(mu_);
  ++moves_;
}

void Host::prev() {
  std::lock_guard lock(mu_);
  --moves_;
}

void Host::on_track(std::uint64_t seed) {
  std::lock_guard lock(mu_);
  track_ = true;
  seed_ = seed;
}

void Host::set_family(std::vector<std::string> prefixes) {
  std::lock_guard lock(mu_);
  family_ = std::move(prefixes);
  family_changed_ = true;
}

void Host::set_pin(std::string id) {
  std::lock_guard lock(mu_);
  pin_ = std::move(id);
  pin_changed_ = true;
}

void Host::set_reduced_motion(bool on) {
  std::lock_guard lock(mu_);
  reduced_ = on;
  reduced_changed_ = true;
}

TexturedQuad Host::frame(Rect dst) const {
  TexturedQuad q;
  q.texture = front_.load(std::memory_order_acquire);
  q.dst = dst;
  // gl's rows run bottom up
  q.uv = Rect{0, 1, 1, -1};
  q.opacity = q.texture ? 1.0f : 0.0f;
  return q;
}

void Host::run() {
  if (!context_->make_current()) {
    log_("viz_context_failed", {}, 0);
    running_ = false;
    return;
  }
  const RendererConfig& rc = renderer_.config();
  bool ok = renderer_.init();
  renderer_.set_auto(false);
  for (auto& p : present_) ok = ok && p.make(rc.width, rc.height);
  if (!ok) log_("viz_init_failed", {}, 0);

  std::vector<float> mono(1024), stereo, ahead;
  std::uint64_t grid_epoch = ~0ULL;
  std::int64_t grid_base = 0;
  const int full_fps = std::max(c_.fps, 1);
  auto period = std::chrono::duration<double>(1.0 / full_fps);
  // the frame's cost, smoothed: over ~13 ms the host halves its rate, under ~7 ms it comes back
  double cost_ms = 0;
  bool halved = false;
  double cost_sum = 0, cost_from = 0, energy_sum = 0;
  int beats_logged = 0;
  int cost_n = 0;
  const auto delay_ns = static_cast<std::int64_t>((c_.delay_ms - c_.lead_ms) * 1e6);
  auto due = std::chrono::steady_clock::now();
  const auto t0 = due;
  std::int64_t last_index = -1;
  std::size_t k = 0;
  FrameInputs in;
  double last_s = 0;
  while (running_ && ok) {
    std::this_thread::sleep_until(due);
    due += std::chrono::duration_cast<std::chrono::steady_clock::duration>(period);
    if (paused_) continue;
    {
      std::lock_guard lock(mu_);
      for (; moves_ > 0; --moves_) renderer_.next();
      for (; moves_ < 0; ++moves_) renderer_.prev();
      if (std::exchange(track_, false)) {
        renderer_.on_track(seed_);
        if (!has_track_ || seed_ != last_track_) {
          features_.on_track();
          last_track_ = seed_;
          has_track_ = true;
        }
      }
      // no switching on a timer (the owner: it switched too often): a new track, a key, or the pin
      if (std::exchange(reduced_changed_, false)) renderer_.set_auto(false);
      if (std::exchange(pin_changed_, false)) {
        renderer_.set_pin(pin_, std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count());
      }
      if (std::exchange(family_changed_, false)) {
        renderer_.set_family(family_, std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count());
      }
    }
    const auto now = std::chrono::steady_clock::now();
    const double now_s = std::chrono::duration<double>(now - t0).count();
    // the frame shows once it is drawn: what it draws is what is heard then, not when it starts (the stick takes
    // 15-30 ms a frame), so its own smoothed cost is looked ahead too; the display's way to the panel is lead_ms's
    const std::int64_t heard_ns = mono_now_ns() - delay_ns + static_cast<std::int64_t>(cost_ms * 1e6);
    timeline_.window(heard_ns, mono);
    // the beat grid takes every frame in order, as far ahead as the timeline holds: a beat is confirmed on its peak
    // before it is heard. a fresh timeline (a new stream, a jump) starts the grid over from what is heard now
    const std::int64_t heard_index = timeline_.index_of(heard_ns);
    if (const auto epoch = timeline_.epoch(); epoch != grid_epoch) {
      grid_epoch = epoch;
      grid_base = heard_index;
      features_.restart(timeline_.rate());
    }
    {
      const std::int64_t from = grid_base + features_.fed();
      const std::int64_t to = std::min(timeline_.newest(), from + static_cast<std::int64_t>(timeline_.rate()));
      if (to > from) {
        const std::int64_t got = timeline_.read(from, to, ahead);
        if (got != from) {
          // the ring moved past what the grid had: it starts over where the ring begins
          grid_base = got;
          features_.restart(timeline_.rate());
        }
        features_.feed(ahead);
      }
    }
    // the frames heard since the last frame, at most the window: projectM's contiguous feed
    const auto index = static_cast<std::int64_t>(std::floor(static_cast<double>(heard_ns) * timeline_.rate() / 1e9));
    const auto fresh = static_cast<std::size_t>(
        std::clamp<std::int64_t>(last_index < 0 ? 0 : index - last_index, 0, static_cast<std::int64_t>(mono.size())));
    last_index = index;
    stereo.resize(fresh * 2);
    for (std::size_t i = 0; i < fresh; ++i) stereo[2 * i] = stereo[2 * i + 1] = mono[mono.size() - fresh + i];
    in.pcm_stereo = stereo;
    features_.update(mono, timeline_.rate(), now_s - last_s, in, std::max<std::int64_t>(heard_index - grid_base, 0));
    energy_sum += in.energy;
    // each beat as it is drawn, its pulse as the value: the log's own time stamps line them up with the song
    if (in.onset) {
      ++beats_logged;
      if (c_.log_beats) log_("viz_beat", renderer_.current(), in.beat_pulse);
    }
    in.time_s = now_s;
    last_s = now_s;
    const auto r0 = std::chrono::steady_clock::now();
    renderer_.render(in, now_s);
    const gl::Target& p = present_[k];
    glBindFramebuffer(GL_READ_FRAMEBUFFER, renderer_.last_fbo());
    glBindFramebuffer(GL_DRAW_FRAMEBUFFER, p.fbo);
    glBlitFramebuffer(0, 0, rc.width, rc.height, 0, 0, rc.width, rc.height, GL_COLOR_BUFFER_BIT, GL_NEAREST);
    glBindFramebuffer(GL_FRAMEBUFFER, 0);
    // finished before the ui samples it from its own context
    glFinish();
    const double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - r0).count();
    cost_ms += (ms - cost_ms) * 0.05;
    // what a frame costs here, every 10 s: the way to tell how a size and a scene fit this gpu
    cost_sum += ms;
    ++cost_n;
    if (now_s - cost_from >= 10) {
      log_("viz_cost", renderer_.current(), cost_sum / static_cast<double>(std::max(cost_n, 1)));
      // how far ahead of what is heard the tap's newest frame is: about the output's latency when the feed keeps up
      // (a group member's too); near zero or negative, the picture runs late
      log_("viz_ahead", renderer_.current(), timeline_.ahead_ms(mono_now_ns()));
      // the feed and the beats over the window: what to compare with the same song rendered offline
      const auto fs = timeline_.take_stats();
      log_("viz_feed_pushes", renderer_.current(), static_cast<double>(fs.pushes));
      if (fs.dropped + fs.resets + fs.gap_frames + fs.missing_frames > 0) {
        log_("viz_feed_dropped", renderer_.current(), static_cast<double>(fs.dropped));
        log_("viz_feed_resets", renderer_.current(), static_cast<double>(fs.resets));
        log_("viz_feed_gap_ms", renderer_.current(), static_cast<double>(fs.gap_frames) * 1000.0 / timeline_.rate());
        log_("viz_feed_missing_ms", renderer_.current(), static_cast<double>(fs.missing_frames) * 1000.0 / timeline_.rate());
      }
      log_("viz_beats", renderer_.current(), static_cast<double>(beats_logged));
      log_("viz_energy", renderer_.current(), energy_sum / std::max(cost_n, 1));
      beats_logged = 0;
      energy_sum = 0;
      cost_sum = 0;
      cost_n = 0;
      cost_from = now_s;
    }
    if (full_fps > 30 && !halved && cost_ms > 13) {
      halved = true;
      period = std::chrono::duration<double>(2.0 / full_fps);
      log_("viz_fps", renderer_.current(), static_cast<double>(full_fps) / 2);
    } else if (halved && cost_ms < 7) {
      halved = false;
      period = std::chrono::duration<double>(1.0 / full_fps);
      log_("viz_fps", renderer_.current(), static_cast<double>(full_fps));
    }
    front_.store(p.texture, std::memory_order_release);
    if (c_.on_frame) c_.on_frame();
    k = (k + 1) % present_.size();
    // a slow frame pushes the next back rather than queueing frames, and one slower than its period leaves the gpu
    // as long again to the ui: a heavy size on a weak gpu (the stick at 1280 x 720) runs slowly, never starves the
    // screen
    const auto end = std::chrono::steady_clock::now();
    const auto spent = end - r0;
    due = std::max(due, spent > period ? end + spent : end);
  }
  front_ = 0;
  renderer_.release();
  for (auto& p : present_) p.release();
  context_->release();
}

}  // namespace cadence::render::viz

#include "renderer.h"

#include <algorithm>
#include <array>
#include <chrono>
#include <cmath>

namespace cadence::render::viz {

Renderer::Renderer(RendererConfig c, LogFn log)
    : c_(std::move(c)),
      log_(std::move(log)),
      scheduler_(c_.schedule),
      budget_(c_.budget_ms, c_.budget_window_s) {
  if (!log_) log_ = [](const std::string&, const std::string&, double) {};
}

Renderer::~Renderer() { release(); }

void Renderer::add(std::unique_ptr<VizSource> source) { sources_.push_back(std::move(source)); }

void Renderer::add_transitions(std::vector<Transition> transitions) {
  for (auto& t : transitions) transition_src_.push_back(std::move(t));
}

bool Renderer::init() {
  glGenTextures(1, &audio_);
  glBindTexture(GL_TEXTURE_2D, audio_);
  glTexImage2D(GL_TEXTURE_2D, 0, GL_R8, static_cast<GLsizei>(kFftBins), 2, 0, GL_RED, GL_UNSIGNED_BYTE, nullptr);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
  if (!slots_[0].make(c_.width, c_.height) || !slots_[1].make(c_.width, c_.height) || !out_.make(c_.width, c_.height)) {
    log_("viz_target_failed", {}, 0);
    return false;
  }
  std::vector<std::string> ids;
  for (auto it = sources_.begin(); it != sources_.end();) {
    if ((*it)->init(c_.width, c_.height)) {
      ids.push_back((*it)->id());
      ++it;
    } else {
      log_("viz_source_failed", (*it)->id(), 0);
      it = sources_.erase(it);
    }
  }
  if (transition_src_.empty() || !transitions_.init(transition_src_)) transitions_.init({crossfade()});
  for (const auto& f : transitions_.failed()) log_("viz_transition_failed", f, 0);
  playlist_ = std::make_unique<Playlist>(ids, c_.slow_file);
  initialized_ = true;
  if (playlist_->empty()) return false;
  begin(playlist_->current(), false, 0);
  return cur_ != nullptr;
}

void Renderer::release() {
  if (!initialized_) return;
  initialized_ = false;
  if (from_) from_->deactivate();
  if (cur_) cur_->deactivate();
  from_ = cur_ = nullptr;
  for (auto& s : sources_) s->release();
  transitions_.release();
  for (auto& t : slots_) t.release();
  out_.release();
  if (audio_) glDeleteTextures(1, &audio_);
  audio_ = 0;
}

VizSource* Renderer::find(const std::string& id) {
  for (auto& s : sources_) {
    if (s->id() == id) return s.get();
  }
  return nullptr;
}

std::vector<std::string> Renderer::ids() const {
  std::vector<std::string> out;
  for (const auto& s : sources_) out.push_back(s->id());
  return out;
}

std::string Renderer::current() const { return cur_ ? cur_->id() : std::string(); }

void Renderer::on_track(std::uint64_t seed) {
  track_seed_ = seed;
  track_asked_ = true;
}

void Renderer::set_family(std::vector<std::string> prefixes, double now_s) {
  if (!playlist_) return;
  playlist_->set_family(std::move(prefixes));
  const auto& want = playlist_->current();
  if (!want.empty() && (!cur_ || cur_->id() != want)) begin(want, cur_ != nullptr, now_s);
}

void Renderer::set_pin(const std::string& id, double now_s) {
  pin_ = id;
  if (id.empty() || !playlist_ || !playlist_->select(id)) return;
  if (!cur_ || cur_->id() != id) begin(id, cur_ != nullptr, now_s);
}

bool Renderer::show(const std::string& id) {
  if (!playlist_ || !playlist_->select(id)) return false;
  begin(id, false, 0);
  return cur_ && cur_->id() == id;
}

void Renderer::begin(const std::string& to, bool transition, double now_s) {
  VizSource* s = find(to);
  if (!s || s == cur_) return;
  if (!s->activate()) {
    // skipped for this run, and the next usable one shown in its place (each failure drops one, so this ends): a
    // preset that fails never leaves the output black, nor the first one the whole host
    log_("viz_source_failed", to, 0);
    if (playlist_->current() != to) return;
    playlist_->drop();
    if (!playlist_->empty()) begin(playlist_->current(), transition, now_s);
    return;
  }
  if (from_) {
    from_->deactivate();
    from_ = nullptr;
  }
  if (transition && cur_) {
    from_ = cur_;
    cur_slot_ ^= 1;
    transition_ = (transition_ + 1) % std::max<std::size_t>(transitions_.size(), 1);
    scheduler_.started(now_s);
  } else if (cur_) {
    cur_->deactivate();
  }
  cur_ = s;
  budget_.reset();
  log_("viz_source", to, 0);
}

void Renderer::upload_audio(const FrameInputs& in) {
  std::array<std::uint8_t, kFftBins * 2> px{};
  for (std::size_t i = 0; i < kFftBins; ++i) {
    px[i] = static_cast<std::uint8_t>(std::lround(std::clamp(in.fft[i], 0.0f, 1.0f) * 255.0f));
    px[kFftBins + i] = static_cast<std::uint8_t>(std::lround(std::clamp(in.wave[i] * 0.5f + 0.5f, 0.0f, 1.0f) * 255.0f));
  }
  glBindTexture(GL_TEXTURE_2D, audio_);
  glPixelStorei(GL_UNPACK_ALIGNMENT, 1);
  glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, static_cast<GLsizei>(kFftBins), 2, GL_RED, GL_UNSIGNED_BYTE, px.data());
}

bool Renderer::draw(VizSource& s, const gl::Target& t, const FrameInputs& in, double now_s) {
  const auto t0 = std::chrono::steady_clock::now();
  t.bind();
  s.render(in, t.fbo);
  // the viz thread may wait: the cost is the source's own
  glFinish();
  const double ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count();
  Cost& c = costs_[s.id()];
  c.last_ms = ms;
  c.mean_ms += (ms - c.mean_ms) / static_cast<double>(++c.frames);
  return &s == cur_ && budget_.over(ms, now_s);
}

unsigned Renderer::render(FrameInputs& in, double now_s) {
  if (!cur_) return 0;
  upload_audio(in);
  in.audio_texture = audio_;
  if (track_asked_) {
    track_asked_ = false;
    if (pin_.empty()) begin(playlist_->shuffle(track_seed_), true, now_s);
  }
  // once the tempo holds, a switch lands on a bar line; before that on any onset, or the older detector's beat
  const bool beat = in.bpm > 0 ? in.bar : (in.onset || in.beat);
  if (auto m = scheduler_.step(now_s, beat)) {
    begin(*m == Scheduler::Move::Next ? playlist_->next() : playlist_->prev(), true, now_s);
  }
  const bool transitioning = from_ && scheduler_.transitioning(now_s);
  if (!transitioning && from_) {
    from_->deactivate();
    from_ = nullptr;
  }
  bool slow = draw(*cur_, slots_[cur_slot_], in, now_s);
  unsigned result = slots_[cur_slot_].texture;
  last_fbo_ = slots_[cur_slot_].fbo;
  if (transitioning) {
    draw(*from_, slots_[cur_slot_ ^ 1], in, now_s);
    out_.bind();
    transitions_.draw(transition_, slots_[cur_slot_ ^ 1].texture, slots_[cur_slot_].texture,
                      static_cast<float>(scheduler_.progress(now_s)), c_.width, c_.height);
    result = out_.texture;
    last_fbo_ = out_.fbo;
  }
  glBindFramebuffer(GL_FRAMEBUFFER, 0);
  // a pinned source stays whatever it costs; so does the last one of the family
  if (slow && (!pin_.empty() && cur_->id() == pin_)) slow = false;
  if (slow && !playlist_->has_other()) slow = false;
  if (slow) {
    const std::string id = cur_->id();
    log_("viz_source_slow", id, costs_[id].last_ms);
    playlist_->mark_slow();
    if (playlist_->empty()) {
      log_("viz_no_sources", {}, 0);
    } else {
      begin(playlist_->current(), true, now_s);
    }
  }
  return result;
}

}  // namespace cadence::render::viz

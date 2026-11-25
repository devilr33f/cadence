#include "ui/core/volume-overlay.h"

#include "ui/core/rml.h"
#include "ui/tokens.h"

#include <RmlUi/Core/ElementDocument.h>
#include <RmlUi/Core/Property.h>
#include <RmlUi/Core/Tween.h>

#include <algorithm>
#include <string>

namespace cadence::screen::ui {

namespace {

// the exit's fade (motion.quick-exit), and a frame, before the pill leaves the layout
constexpr std::int64_t kExitMs = tokens::kMotionQuickExitMs + 20;

}  // namespace

VolumeOverlay::VolumeOverlay(Ui& ui) : ui_(ui) {
  ui_.addDocument(
      "volume", kZ,
      [] {
        return "<div id=\"vol\" class=\"vol hidden\"><span id=\"volicon\" class=\"volicon\">" + rml::icon("volume") +
               "</span><div class=\"voltrack\"><div id=\"volfill\" class=\"volfill\"/></div>"
               "<span id=\"volnum\" class=\"volnum\">0</span></div>";
      },
      [this](Rml::ElementDocument& d) { loaded(d); });
}

VolumeOverlay::~VolumeOverlay() { ui_.removeDocument("volume"); }

Rml::Element* VolumeOverlay::el(const char* id) const { return doc_ != nullptr ? doc_->GetElementById(id) : nullptr; }

void VolumeOverlay::loaded(Rml::ElementDocument& doc) {
  doc_ = &doc;
  // a language switch rebuilds the layer: what shows stays, without its entrance again
  render(false);
}

void VolumeOverlay::message(const cadence_screen_v1_Envelope& m) {
  if (m.which_body != cadence_screen_v1_Envelope_volume_state_tag) return;
  const auto& v = m.body.volume_state;
  const int level = static_cast<int>(std::min<std::uint32_t>(v.level, 100));
  if (!v.show) {
    // the state as the screen attaches: kept for the next change, nothing shown
    level_ = level;
    muted_ = v.muted;
    if (visible_) render(false);
    return;
  }
  show(level, v.muted);
}

void VolumeOverlay::show(int level, bool muted) {
  const bool entering = !visible_;
  level_ = level;
  muted_ = muted;
  visible_ = true;
  const auto seq = ++seq_;
  render(entering);
  std::weak_ptr<int> alive = alive_;
  ui_.after(kVolumeShowMs, [this, alive, seq] {
    if (!alive.expired()) hide(seq);
  });
}

void VolumeOverlay::hide(std::uint64_t seq) {
  if (seq != seq_ || !visible_) return;
  visible_ = false;
  if (auto* p = el("vol")) p->SetClass("leaving", true);
  ui_.markDirty();
  std::weak_ptr<int> alive = alive_;
  ui_.after(kExitMs, [this, alive, seq] {
    if (alive.expired() || seq != seq_ || visible_) return;
    if (auto* p = el("vol")) {
      p->SetClass("hidden", true);
      p->SetClass("leaving", false);
    }
    ui_.markDirty();
  });
}

void VolumeOverlay::render(bool animate) {
  auto* pill = el("vol");
  if (pill == nullptr) return;
  const bool silent = muted_ || level_ == 0;
  pill->SetClass("muted", silent);
  pill->SetClass("leaving", false);
  pill->SetClass("hidden", !visible_);
  if (auto* icon = el("volicon")) icon->SetInnerRML(rml::icon(silent ? "mute" : "volume"));
  if (auto* num = el("volnum")) num->SetInnerRML(std::to_string(level_));
  if (auto* fill = el("volfill")) {
    const Rml::Property height(static_cast<float>(level_), Rml::Unit::PERCENT);
    // rmlui transitions only class changes: a change while shown glides by an animation of its own, an entrance starts
    // at its level
    if (animate || !visible_ || ui_.reducedMotion() || fill->GetLocalProperty("height") == nullptr) {
      fill->SetProperty(Rml::PropertyId::Height, height);
    } else {
      fill->Animate("height", height, static_cast<float>(tokens::kMotionGlideMs) / 1000.0f,
                    Rml::Tween{Rml::Tween::Cubic, Rml::Tween::Out});
    }
  }
  if (animate) enter();
  ui_.markDirty();
}

// one update in the entering pose, so rmlui has it computed, then the class goes and its transition runs
void VolumeOverlay::enter() {
  if (auto* p = el("vol")) p->SetClass("entering", true);
  std::weak_ptr<int> alive = alive_;
  const auto seq = seq_;
  ui_.after(0, [this, alive, seq] {
    if (alive.expired()) return;
    ui_.after(0, [this, alive, seq] {
      if (alive.expired()) return;
      if (auto* p = el("vol"); p != nullptr && seq == seq_) p->SetClass("entering", false);
      ui_.markDirty();
    });
  });
}

}  // namespace cadence::screen::ui

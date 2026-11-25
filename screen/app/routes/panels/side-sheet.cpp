#include "app/routes/panels/side-sheet.h"

#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>

namespace cadence::screen::routes {

void SideSheet::open(std::unique_ptr<SheetContent> content, Rml::Element* opener) {
  if (content_) close();
  auto* host = c_.el("side-host");
  if (host == nullptr) return;
  content_ = std::move(content);
  opener_ = opener != nullptr ? opener->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  scroll_ = 0;
  host->SetInnerRML("<div class=\"side entering\" id=\"side\"><div class=\"shead line1\">" + content_->title() +
                    "</div><div class=\"shint\">[[panel.close_hint]]</div><div class=\"sbody\" id=\"side-body\"/></div>");
  auto* side = host->GetElementById("side");
  layer_ = side->GetObserverPtr();
  content_->open(*this, host->GetElementById("side-body"));
  c_.ui().focus().pushScope(side);
  // the entrance runs once it has been laid out closed
  c_.ui().after(0, [l = layer_] {
    if (auto* e = l.get()) e->SetClass("entering", false);
  });
  if (auto* f = c_.ui().focus().current()) scrollTo(f);
  c_.ui().markDirty();
}

void SideSheet::close() {
  if (!content_) return;
  content_.reset();
  c_.ui().focus().popScope();
  if (auto* o = opener_.get(); o != nullptr && o->IsVisible(true)) c_.ui().focus().focus(o);
  if (auto* host = c_.el("side-host")) host->SetInnerRML("");
  layer_.reset();
  opener_.reset();
  c_.ui().markDirty();
  if (onClosed) onClosed();
}

void SideSheet::scrollTo(Rml::Element* e) {
  auto* body = c_.el("side-body");
  auto* side = layer_.get();
  if (body == nullptr || side == nullptr || e == nullptr) return;
  const float dp = c_.ui().dp();
  // the element's top inside the body, unscrolled; the body's visible height is the sheet's below its header
  const float top = e->GetAbsoluteOffset(Rml::BoxArea::Border).y - body->GetAbsoluteOffset(Rml::BoxArea::Border).y - scroll_;
  const float visible = side->GetBox().GetSize().y - (body->GetAbsoluteOffset().y - side->GetAbsoluteOffset().y);
  const float content = body->GetBox().GetSize(Rml::BoxArea::Border).y;
  const float want = std::clamp(top - visible / 3, 0.0f, std::max(0.0f, content - visible + 40 * dp));
  if (std::abs(want - scroll_) < 0.5f) return;
  scroll_ = want;
  body->SetProperty("transform", "translateY(" + std::to_string(-scroll_ / dp) + "dp)");
}

bool SideSheet::key(const platform::KeyEvent& e) {
  if (!content_) return false;
  // a dialog or the actions sheet over this one takes its keys first
  if (c_.ui().levelOpen()) return c_.ui().key(e);
  if (e.key == cadence_screen_v1_Key_KEY_BACK) {
    if (isPress(e)) close();
    return true;
  }
  auto* before = c_.ui().focus().current();
  const bool used = c_.ui().key(e);
  if (auto* now = c_.ui().focus().current(); now != before && now != nullptr) {
    scrollTo(now);
    if (content_) content_->focused(now);
  }
  return used || true;
}

bool SideSheet::activate(const ui::Activation& a) { return content_ && content_->activate(a); }

bool SideSheet::message(const cadence_screen_v1_Envelope& m) { return content_ && content_->message(m); }

void SideSheet::tick(std::int64_t nowMs) {
  if (content_) content_->tick(nowMs);
}

}  // namespace cadence::screen::routes

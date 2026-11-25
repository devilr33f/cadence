#include "app/routes/audio/queue-sidebar.h"

#include "ui/core/rml.h"

#include <nlohmann/json.hpp>

#include <algorithm>
#include <charconv>
#include <cmath>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;

constexpr std::string_view kRowAction = "audio:queue-song:";

}  // namespace

void AudioQueueSidebar::enter() {
  const std::string id = c_.session && !c_.session->device ? c_.session->sessionId : std::string();
  if (id == sessionId_) return;
  sessionId_ = id;
  ++generation_;
  asked_ = false;
  queue_.reset();
  scroll_ = 0;
}

void AudioQueueSidebar::load(bool force) {
  if (!c_.session || c_.session->device) return;
  const std::string id = c_.session->sessionId;
  if (id != sessionId_) enter();
  if (!force && asked_ && id == sessionId_) return;
  sessionId_ = id;
  asked_ = true;
  const auto generation = ++generation_;
  nlohmann::json args = {{"sessionId", id}, {"all", true}};
  c_.rpc.call("media.queue", args.dump(), [this, id, generation](const RpcAnswer& a) {
    if (generation != generation_ || id != sessionId_ || !c_.session || c_.session->sessionId != id) return;
    const bool was = visible();
    queue_ = a.ok() ? audioQueueFrom(a.json) : std::nullopt;
    scroll_ = 0;
    render();
    // a long queue opens with its playing row in view, without taking focus from the transport
    if (!was && visible()) scrollTo(c_.el("aq-item-" + std::to_string(queue_->position)));
  }, c_.nowMs);
}

std::string AudioQueueSidebar::body() const {
  if (!visible()) return {};
  const auto& q = *queue_;
  std::string s = "<div class=\"aqueue\" id=\"a-queue\"><div class=\"aqhead\">";
  if (!q.art.empty()) {
    s += ui::rml::artImage(q.art, "aqcover");
  } else {
    s += "<div class=\"aqcover placeholder\">" + ui::rml::icon("playlist") + "</div>";
  }
  s += "<div class=\"aqheading\"><div class=\"aqtitle\">" + (q.name.empty() ? std::string("[[audio.queue]]") : esc(q.name)) +
       "</div><div class=\"aqcount\">[[audio.queue_count|count=" + std::to_string(q.total) + "]]</div></div></div>";
  s += "<div class=\"aqview\" id=\"aq-view\"><div class=\"aqlist vscroll manual\" id=\"aq-list\">";
  for (std::size_t i = 0; i < q.items.size(); ++i) {
    const auto& item = q.items[i];
    ui::rml::ListItem row;
    row.id = "aq-item-" + std::to_string(i);
    row.title = esc(item.title);
    row.meta = esc(item.subtitle);
    row.leadArt = !item.art.empty() ? item.art : q.art;
    if (row.leadArt.empty()) row.leadIcon = "music";
    if (item.durationMs > 0) row.trail = "<span class=\"figures\">" + clockTime(item.durationMs) + "</span>";
    row.action = std::string(kRowAction) + std::to_string(i);
    if (i == q.position) row.classes = "current";
    s += ui::rml::listItem(row);
  }
  return s + "</div></div></div>";
}

void AudioQueueSidebar::render() {
  if (auto* root = c_.el("audio")) root->SetClass("queued", visible());
  std::string focused;
  if (auto* f = c_.ui().focus().current(); f != nullptr && f->IsVisible(true)) focused = f->GetId();
  if (auto* host = c_.el("a-queue-host")) host->SetInnerRML(body());
  if (!focused.empty()) {
    if (auto* f = c_.el(focused)) {
      c_.ui().focus().focus(f);
      if (focused.starts_with("aq-item-")) scrollTo(f);
    }
  } else if (auto* f = c_.ui().focus().current(); f != nullptr && f->GetId().starts_with("aq-item-")) {
    // SetInnerRML focuses the first row: lean-back has no focused row until the remote wakes the controls
    f->Blur();
  }
  c_.ui().markDirty();
}

bool AudioQueueSidebar::activate(const ui::Activation& a) {
  if (!visible() || !a.action.starts_with(kRowAction)) return false;
  std::size_t index = 0;
  const auto at = a.action.substr(kRowAction.size());
  const auto [end, err] = std::from_chars(at.data(), at.data() + at.size(), index);
  if (err != std::errc{} || end != at.data() + at.size() || index >= queue_->items.size()) return true;
  // selecting the song already playing changes nothing; every other row is a queue ref the server can jump to
  if (index == queue_->position) return true;
  const auto& item = queue_->items[index];
  if (c_.transport && !item.ref.empty()) c_.transport("jump", item.ref, nullptr);
  return true;
}

void AudioQueueSidebar::keepVisible() {
  auto* f = c_.ui().focus().current();
  if (f != nullptr && f->GetId().starts_with("aq-item-")) scrollTo(f);
}

void AudioQueueSidebar::scrollTo(Rml::Element* e) {
  auto* view = c_.el("aq-view");
  auto* list = c_.el("aq-list");
  if (e == nullptr || view == nullptr || list == nullptr) return;
  const float dp = c_.ui().dp();
  const float top = e->GetAbsoluteOffset(Rml::BoxArea::Border).y - list->GetAbsoluteOffset(Rml::BoxArea::Border).y - scroll_;
  const float height = e->GetBox().GetSize(Rml::BoxArea::Border).y;
  const float available = view->GetBox().GetSize(Rml::BoxArea::Border).y;
  float want = scroll_;
  if (top < 0) want += top - 8.0f * dp;
  if (top + height > available) want += top + height - available + 8.0f * dp;
  const float content = list->GetBox().GetSize(Rml::BoxArea::Border).y;
  want = std::clamp(want, 0.0f, std::max(0.0f, content - available));
  if (std::abs(want - scroll_) < 0.5f) return;
  scroll_ = want;
  list->SetProperty("transform", "translateY(" + std::to_string(-scroll_ / dp) + "dp)");
}

}  // namespace cadence::screen::routes

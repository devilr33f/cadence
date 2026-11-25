#include "ui/images/binder.h"

#include <RmlUi/Core/ComputedValues.h>
#include <RmlUi/Core/Core.h>
#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <cmath>

namespace cadence::screen::ui::images {

ImageBinder::ImageBinder(ImageCache& cache, std::size_t uploadBudget)
    : cache_(cache), uploadBudget_(uploadBudget), budgetLeft_(uploadBudget) {
  cache_.onEvict([this](const std::string& key) {
    const auto it = sources_.find(key);
    if (it == sources_.end()) return;
    Rml::ReleaseTexture(it->second);
    sources_.erase(it);
  });
}

ImageBinder::~ImageBinder() {
  cache_.onEvict(nullptr);
  for (auto& b : bound_) cache_.unpin(b.key);
}

std::string ImageBinder::keyOfSource(const std::string& source) {
  if (!source.starts_with("cimg:")) return {};
  const auto hash = source.find('#');
  return source.substr(5, hash == std::string::npos ? std::string::npos : hash - 5);
}

void ImageBinder::release() {
  for (auto it = bound_.begin(); it != bound_.end();) {
    auto* e = it->element.get();
    const bool gone = e == nullptr || e->GetOwnerDocument() == nullptr;
    // an element whose image was lost is unbound too, so the next scan asks again
    const bool lost = !gone && lostKeys_.count(it->key) != 0;
    if (gone || lost) {
      if (lost) {
        e->RemoveAttribute("data-img-key");
        e->RemoveAttribute("data-img-src");
        e->RemoveProperty("decorator");
        e->SetClass("bound", false);
      }
      cache_.unpin(it->key);
      it = bound_.erase(it);
    } else {
      ++it;
    }
  }
  lostKeys_.clear();
}

bool ImageBinder::scan(Rml::Element* root) {
  release();
  if (root == nullptr) return false;
  Rml::ElementList list;
  root->QuerySelectorAll(list, ".netimg");
  bool changed = false;
  waiting_ = 0;
  for (auto* e : list) {
    if (e->HasAttribute("data-img-key") || e->HasAttribute("data-img-failed")) continue;
    const auto id = e->GetAttribute<Rml::String>("data-img", "");
    const auto size = e->GetBox().GetSize(Rml::BoxArea::Border);
    // not laid out yet, or hidden: the next scan
    if (size.x < 1 || size.y < 1) continue;
    int w = static_cast<int>(std::ceil(size.x));
    int h = static_cast<int>(std::ceil(size.y));
    // a large backdrop decodes smaller and is scaled up (it sits at 35 %: the softness is invisible)
    if (const int max = e->GetAttribute<int>("data-img-max", 0); max > 0 && w > max) {
      h = h * max / w;
      w = max;
    }
    std::string key;
    // a slot with rounded corners gets its picture rounded at decode: its decorator draws it whole, no clip mask
    const int radius = static_cast<int>(std::lround(e->GetComputedValues().border_top_left_radius()));
    const auto state = cache_.request(id, w, h, &key, e->GetAttribute<int>("data-img-blur", 0), radius);
    if (state == State::Failed) {
      e->SetAttribute("data-img-failed", "1");
      continue;
    }
    if (state != State::Ready && state != State::Resident) {
      waiting_++;
      continue;
    }
    // pixels still on the cpu go to the gpu at the next render: the pass's budget spreads them over frames
    if (const auto bytes = cache_.readyBytes(key); bytes > 0) {
      if (boundThisPass_ && bytes > budgetLeft_) {
        deferred_++;
        continue;
      }
      budgetLeft_ -= std::min(bytes, budgetLeft_);
      boundThisPass_ = true;
    }
    auto& source = sources_[key];
    if (source.empty()) source = "cimg:" + key + "#" + std::to_string(++gen_);
    cache_.pin(key);
    bound_.push_back(Bound{e->GetObserverPtr(), key});
    e->SetAttribute("data-img-key", key);
    e->SetAttribute("data-img-src", source);
    e->SetProperty("decorator", "image(\"" + source + "\" cover)");
    // a route may wait for it (home's backdrop cross-fades in once its image is bound)
    e->SetClass("bound", true);
    changed = true;
  }
  return changed;
}

bool ImageBinder::poll() {
  // a new pass: its upload budget, and what the last one held back is due now
  const bool held = deferred_ > 0;
  budgetLeft_ = uploadBudget_;
  boundThisPass_ = false;
  deferred_ = 0;
  return !cache_.poll().empty() || !lostKeys_.empty() || held;
}

void ImageBinder::lost(const std::string& source) {
  const auto key = keyOfSource(source);
  if (key.empty()) return;
  lostKeys_.insert(key);
  cache_.lost(key);
  // a fresh source, so rmlui loads it anew once decoded again
  sources_.erase(key);
}

}  // namespace cadence::screen::ui::images

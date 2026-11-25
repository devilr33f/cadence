#include "ui/core/budget.h"

#include <RmlUi/Core/ComputedValues.h>
#include <RmlUi/Core/Element.h>
#include <RmlUi/Core/ElementDocument.h>

#include <functional>

namespace cadence::screen::ui {

namespace {

std::string nameOf(Rml::Element* e) {
  if (!e->GetId().empty()) return "#" + e->GetId();
  const auto cls = e->GetClassNames();
  return cls.empty() ? e->GetTagName() : "." + cls.substr(0, cls.find(' '));
}

bool singleLine(Rml::Element* e) {
  return e->IsClassSet("line1") || e->IsClassSet("title") || e->IsClassSet("ititle") || e->IsClassSet("btn") ||
         e->IsClassSet("chip") || e->IsClassSet("badge") || e->IsClassSet("ctitle") || e->IsClassSet("ttext") ||
         e->IsClassSet("key");
}

}  // namespace

LayerReport analyseLayers(Rml::Context& context) {
  LayerReport r;
  const auto dims = context.GetDimensions();
  const float screen = static_cast<float>(dims.x) * static_cast<float>(dims.y);
  std::function<void(Rml::Element*)> walk = [&](Rml::Element* e) {
    if (!e->IsVisible()) return;
    const auto& cv = e->GetComputedValues();
    const auto size = e->GetBox().GetSize(Rml::BoxArea::Border);
    const auto pos = e->GetAbsoluteOffset(Rml::BoxArea::Border);
    if (size.x * size.y >= 0.9f * screen && cv.opacity() > 0.01f) {
      const auto a = cv.background_color().alpha;
      const bool translucentFill = a > 0 && (a < 255 || cv.opacity() < 0.99f);
      if (translucentFill || cv.has_decorator()) {
        r.translucentLayers++;
        r.layers.push_back(nameOf(e));
      }
    }
    if (singleLine(e) && size.y > 0) {
      const float line = cv.line_height().value;
      // a single-line slot whose content box is taller than one line wrapped
      if (line > 0 && e->GetBox().GetSize(Rml::BoxArea::Content).y > line * 1.5f && !e->IsClassSet("btn")) {
        r.overflows++;
        r.overflowing.push_back(nameOf(e));
      }
    }
    if (pos.x + size.x > static_cast<float>(dims.x) + 1 && e->IsClassSet("ctext")) {
      r.overflows++;
      r.overflowing.push_back(nameOf(e));
    }
    for (int i = 0; i < e->GetNumChildren(); ++i) walk(e->GetChild(i));
  };
  for (int i = 0; i < context.GetNumDocuments(); ++i) {
    auto* doc = context.GetDocument(i);
    if (doc != nullptr && doc->IsVisible()) walk(doc);
  }
  return r;
}

}  // namespace cadence::screen::ui

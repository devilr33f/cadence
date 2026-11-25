#pragma once

#include "../analyzer.h"
#include "../classic.h"
#include "gl_canvas.h"
#include "source.h"

namespace cadence::render::viz {

// the classic styles as a source: the Analyzer fed the frame's waveform (the frames heard), drawn by Classic
// through a GlCanvas on the ink
class ClassicSource final : public VizSource {
 public:
  explicit ClassicSource(Style style, AnalyzerConfig ac = {}, ClassicLook look = {});

  [[nodiscard]] std::string id() const override;
  bool init(int width, int height) override;
  void render(const FrameInputs& in, unsigned fbo) override;
  void release() override { canvas_.release(); }

 private:
  Style style_;
  Analyzer analyzer_;
  Classic classic_;
  GlCanvas canvas_;
};

}  // namespace cadence::render::viz

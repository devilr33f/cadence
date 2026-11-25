#include "classic_source.h"

#include "gl.h"

namespace cadence::render::viz {

ClassicSource::ClassicSource(Style style, AnalyzerConfig ac, ClassicLook look)
    : style_(style), analyzer_(ac), classic_(look) {}

std::string ClassicSource::id() const {
  switch (style_) {
    case Style::Mirror:
      return "classic:mirror";
    case Style::Scope:
      return "classic:scope";
    case Style::Bars:
      break;
  }
  return "classic:bars";
}

bool ClassicSource::init(int width, int height) { return canvas_.init(width, height); }

void ClassicSource::render(const FrameInputs& in, unsigned fbo) {
  glBindFramebuffer(GL_FRAMEBUFFER, fbo);
  glViewport(0, 0, canvas_.width(), canvas_.height());
  analyzer_.push_pcm(in.wave, 1, 48000);
  analyzer_.step(in.dt);
  canvas_.clear(Color{0.05f, 0.04f, 0.05f, 1});
  const float w = static_cast<float>(canvas_.width()), h = static_cast<float>(canvas_.height());
  classic_.draw(canvas_, Rect{w * 0.05f, h * 0.2f, w * 0.9f, h * 0.6f}, analyzer_, style_);
}

}  // namespace cadence::render::viz

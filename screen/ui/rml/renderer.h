#pragma once

#include "RmlUi_Renderer_GL3.h"
#include "ui/rml/files.h"
#include "ui/rml/sprites.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <string>

namespace cadence::screen::ui {

// rmlui's gl3 renderer (patched: gles 3, no msaa layer, an external base target) drawing into the ui layer's
// render target, which A's RenderBackend made and composites. it loads textures through the ui's files (png and
// jpeg by stb_image, premultiplied) and draws the "gen:" sprites itself
class RmlRenderer final : public RenderInterface_GL3 {
 public:
  explicit RmlRenderer(RmlFiles& files) : files_(files) {}

  // "cimg:" sources come from the image cache: source fills the pixels (false when it holds none, and then lost is
  // told, so the image is fetched again)
  using ImageSource = std::function<bool(const std::string& source, Pixels& out)>;
  void setImageSource(ImageSource source, std::function<void(const std::string&)> lost) {
    images_ = std::move(source);
    lost_ = std::move(lost);
  }

  Rml::TextureHandle LoadTexture(Rml::Vector2i& dimensions, const Rml::String& source) override;
  Rml::TextureHandle GenerateTexture(Rml::Span<const Rml::byte> data, Rml::Vector2i dimensions) override;
  void ReleaseTexture(Rml::TextureHandle texture) override;
  void RenderGeometry(Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation, Rml::TextureHandle texture) override;
  void EnableScissorRegion(bool enable) override;
  void SetScissorRegion(Rml::Rectanglei region) override;
  void RenderToClipMask(Rml::ClipMaskOperation operation, Rml::CompiledGeometryHandle geometry,
                        Rml::Vector2f translation) override;
  Rml::LayerHandle PushLayer() override;
  void RenderShader(Rml::CompiledShaderHandle shader, Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation,
                    Rml::TextureHandle texture) override;

  struct Stats {
    std::size_t textures = 0;
    std::size_t textureBytes = 0;
    std::size_t loadFailures = 0;
    // draw calls since the last resetFrame
    std::size_t draws = 0;
  };
  [[nodiscard]] const Stats& stats() const { return stats_; }
  // a frame into a target of this size begins (the fill counter clips the draws to it)
  void resetFrame(int width = 0, int height = 0) {
    stats_.draws = 0;
    targetW_ = static_cast<float>(width);
    targetH_ = static_cast<float>(height);
  }

 private:
  RmlFiles& files_;
  Stats stats_;
  ImageSource images_;
  std::function<void(const std::string&)> lost_;

  // the frame profile's fill estimate: a draw's bounds on the target, inside the scissor
  [[nodiscard]] std::uint64_t coveredPx(Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation) const;
  float targetW_ = 0, targetH_ = 0;
  bool scissorOn_ = false;
  Rml::Rectanglei scissor_;
};

}  // namespace cadence::screen::ui

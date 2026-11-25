#include "ui/rml/renderer.h"

#include "render/perf.h"
#include "ui/rml/sprites.h"

#define STB_IMAGE_IMPLEMENTATION
#define STBI_ONLY_PNG
#define STBI_ONLY_JPEG
#define STBI_NO_STDIO
#if defined(__GNUC__)
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wconversion"
#pragma GCC diagnostic ignored "-Wsign-conversion"
#pragma GCC diagnostic ignored "-Wshadow"
#pragma GCC diagnostic ignored "-Wdouble-promotion"
#pragma GCC diagnostic ignored "-Wunused-function"
#endif
#include <stb/stb_image.h>
#if defined(__GNUC__)
#pragma GCC diagnostic pop
#endif

#include <string>
#include <unordered_map>

namespace cadence::screen::ui {

namespace {

// texture handle -> bytes, for the stats
std::unordered_map<Rml::TextureHandle, std::size_t>& sizes() {
  static std::unordered_map<Rml::TextureHandle, std::size_t> s;
  return s;
}

}  // namespace

Rml::TextureHandle RmlRenderer::LoadTexture(Rml::Vector2i& dimensions, const Rml::String& source) {
  if (source.starts_with("cimg:")) {
    Pixels px;
    if (!images_ || !images_(source, px) || px.width <= 0) {
      if (lost_) lost_(source);
      return 0;
    }
    dimensions = {px.width, px.height};
    return GenerateTexture({px.rgba.data(), px.rgba.size()}, dimensions);
  }
  const auto start = render::perf::nowNs();
  if (auto sprite = generateSprite(source)) {
    render::perf::add(render::perf::Span::Decode, render::perf::nowNs() - start);
    dimensions = {sprite->width, sprite->height};
    return GenerateTexture({sprite->rgba.data(), sprite->rgba.size()}, dimensions);
  }
  std::string bytes;
  if (!files_.read(source, bytes)) {
    stats_.loadFailures++;
    return 0;
  }
  int w = 0, h = 0, n = 0;
  stbi_uc* data = stbi_load_from_memory(reinterpret_cast<const stbi_uc*>(bytes.data()), static_cast<int>(bytes.size()),
                                        &w, &h, &n, 4);
  if (data == nullptr) {
    stats_.loadFailures++;
    return 0;
  }
  const std::size_t count = static_cast<std::size_t>(w) * static_cast<std::size_t>(h);
  for (std::size_t i = 0; i < count; ++i) {
    auto* p = data + i * 4;
    const unsigned a = p[3];
    for (int k = 0; k < 3; ++k) p[k] = static_cast<stbi_uc>((p[k] * a + 127) / 255);
  }
  dimensions = {w, h};
  render::perf::add(render::perf::Span::Decode, render::perf::nowNs() - start);
  const auto handle = GenerateTexture({data, count * 4}, dimensions);
  stbi_image_free(data);
  return handle;
}

Rml::TextureHandle RmlRenderer::GenerateTexture(Rml::Span<const Rml::byte> data, Rml::Vector2i dimensions) {
  const auto start = render::perf::nowNs();
  const auto handle = RenderInterface_GL3::GenerateTexture(data, dimensions);
  render::perf::add(render::perf::Span::Upload, render::perf::nowNs() - start);
  if (handle != 0) {
    const auto bytes = static_cast<std::size_t>(dimensions.x) * static_cast<std::size_t>(dimensions.y) * 4;
    sizes()[handle] = bytes;
    stats_.textures++;
    stats_.textureBytes += bytes;
  }
  return handle;
}

void RmlRenderer::ReleaseTexture(Rml::TextureHandle texture) {
  if (const auto it = sizes().find(texture); it != sizes().end()) {
    stats_.textures--;
    stats_.textureBytes -= it->second;
    sizes().erase(it);
  }
  RenderInterface_GL3::ReleaseTexture(texture);
}

std::uint64_t RmlRenderer::coveredPx(Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation) const {
  auto r = ScreenBounds(geometry, translation);
  if (!r.Valid()) return 0;
  if (scissorOn_) r = r.Intersect(scissor_);
  if (!r.Valid() || r.Width() <= 0 || r.Height() <= 0) return 0;
  return static_cast<std::uint64_t>(r.Width()) * static_cast<std::uint64_t>(r.Height());
}

void RmlRenderer::RenderGeometry(Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation,
                                 Rml::TextureHandle texture) {
  // what the base culls (wholly outside the target or the scissor) is neither a draw nor fill
  if (const auto px = GeometryTransparent(geometry) ? 0 : coveredPx(geometry, translation); px > 0) {
    stats_.draws++;
    render::perf::count(render::perf::Counter::Draws, 1);
    render::perf::count(render::perf::Counter::FillKpx, px / 1000);
  }
  RenderInterface_GL3::RenderGeometry(geometry, translation, texture);
}

void RmlRenderer::EnableScissorRegion(bool enable) {
  scissorOn_ = enable;
  RenderInterface_GL3::EnableScissorRegion(enable);
}

void RmlRenderer::SetScissorRegion(Rml::Rectanglei region) {
  scissor_ = region;
  RenderInterface_GL3::SetScissorRegion(region);
}

void RmlRenderer::RenderToClipMask(Rml::ClipMaskOperation operation, Rml::CompiledGeometryHandle geometry,
                                   Rml::Vector2f translation) {
  render::perf::count(render::perf::Counter::ClipMasks, 1);
  RenderInterface_GL3::RenderToClipMask(operation, geometry, translation);
  render::perf::count(render::perf::Counter::StencilClearKpx, TakeStencilClearedPixels() / 1000);
}

void RmlRenderer::RenderShader(Rml::CompiledShaderHandle shader, Rml::CompiledGeometryHandle geometry,
                               Rml::Vector2f translation, Rml::TextureHandle texture) {
  if (const auto px = GeometryTransparent(geometry) ? 0 : coveredPx(geometry, translation); px > 0) {
    render::perf::count(render::perf::Counter::Gradients, 1);
    render::perf::count(render::perf::Counter::GradientKpx, px / 1000);
    render::perf::count(render::perf::Counter::FillKpx, px / 1000);
  }
  RenderInterface_GL3::RenderShader(shader, geometry, translation, texture);
}

Rml::LayerHandle RmlRenderer::PushLayer() {
  render::perf::count(render::perf::Counter::Layers, 1);
  return RenderInterface_GL3::PushLayer();
}

}  // namespace cadence::screen::ui

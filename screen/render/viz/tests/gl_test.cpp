// the host's gl half on mesa's llvmpipe (make screen-p-gl-test): the shader presets' golden images from the fixed pcm,
// a transition halfway, the classic source, a broken preset dropped, a slow source skipped
#include "fixture.h"
#include "host/bench/egl_offscreen.h"
#include "host/classic_source.h"
#include "host/features.h"
#include "host/strobe.h"
#include "host/renderer.h"
#include "host/scene_source.h"
#include "host/shader_source.h"
#include "milkdrop/milkdrop_sources.h"
#include "raster.h"

#include <catch2/catch_test_macros.hpp>
#include <zlib.h>

#include <chrono>
#include <cstdlib>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <iterator>
#include <thread>

namespace viz = cadence::render::viz;

namespace {

constexpr int kW = 320, kH = 180;

bool context() {
  static const bool ok = viz::egl_offscreen();
  return ok;
}

// the fixture fed as the host feeds it: 1024 frames heard up to frame k * 1600 (30 fps), the new 1600 for projectM
void feed(viz::AudioFeatures& f, viz::FrameInputs& in, int k, std::vector<float>& slice) {
  const long end = 1600L * k + 1024;
  const auto st = viz::fixture::stream(end - 1024, 1024);
  std::vector<float> mono(1024);
  for (std::size_t i = 0; i < 1024; ++i) mono[i] = 0.5f * (st[2 * i] + st[2 * i + 1]);
  slice = viz::fixture::stream(end - 1600, 1600);
  in.pcm_stereo = slice;
  in.time_s = k / 30.0;
  f.update(mono, 48000, 1.0 / 30, in);
}

std::vector<std::uint8_t> read_rgba(unsigned fbo) {
  std::vector<std::uint8_t> px(static_cast<std::size_t>(kW) * kH * 4), flipped(px.size());
  glBindFramebuffer(GL_READ_FRAMEBUFFER, fbo);
  glReadPixels(0, 0, kW, kH, GL_RGBA, GL_UNSIGNED_BYTE, px.data());
  // gl's rows run bottom up; images top down
  const std::size_t row = static_cast<std::size_t>(kW) * 4;
  for (int y = 0; y < kH; ++y) {
    std::memcpy(&flipped[static_cast<std::size_t>(y) * row], &px[static_cast<std::size_t>(kH - 1 - y) * row], row);
  }
  return flipped;
}

// a png that encode_png wrote (filters 0 and 1 only) back to rgba
std::vector<std::uint8_t> decode_png(const std::vector<std::uint8_t>& png, int w, int h) {
  std::vector<std::uint8_t> idat;
  for (std::size_t i = 8; i + 12 <= png.size();) {
    const std::uint32_t n = (std::uint32_t{png[i]} << 24) | (std::uint32_t{png[i + 1]} << 16) |
                            (std::uint32_t{png[i + 2]} << 8) | png[i + 3];
    if (std::memcmp(&png[i + 4], "IDAT", 4) == 0) idat.insert(idat.end(), png.begin() + static_cast<std::ptrdiff_t>(i + 8),
                                                          png.begin() + static_cast<std::ptrdiff_t>(i + 8 + n));
    i += 12 + n;
  }
  const std::size_t stride = static_cast<std::size_t>(w) * 4;
  std::vector<std::uint8_t> raw((stride + 1) * static_cast<std::size_t>(h));
  uLongf len = raw.size();
  if (uncompress(raw.data(), &len, idat.data(), idat.size()) != Z_OK || len != raw.size()) return {};
  std::vector<std::uint8_t> out(stride * static_cast<std::size_t>(h));
  for (std::size_t y = 0; y < static_cast<std::size_t>(h); ++y) {
    const std::uint8_t filter = raw[y * (stride + 1)];
    for (std::size_t x = 0; x < stride; ++x) {
      std::uint8_t v = raw[y * (stride + 1) + 1 + x];
      if (filter == 1 && x >= 4) v = static_cast<std::uint8_t>(v + out[y * stride + x - 4]);
      out[y * stride + x] = v;
    }
  }
  return out;
}

// within 3 levels a channel, and at most 0.5 % of the pixels further off (llvmpipe's rounding may move a little)
void golden(const std::string& name, const std::vector<std::uint8_t>& rgba) {
  const std::vector<std::uint8_t> png = viz::encode_png(kW, kH, rgba);
  const auto file = std::filesystem::path(CADENCE_VIZ_GOLDEN) / (name + ".png");
  if (const char* u = std::getenv("CADENCE_UPDATE_GOLDEN"); u && std::string(u) == "1") {
    std::ofstream(file, std::ios::binary).write(reinterpret_cast<const char*>(png.data()),
                                                static_cast<std::streamsize>(png.size()));
    return;
  }
  std::ifstream in(file, std::ios::binary);
  const std::vector<std::uint8_t> want_png((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
  const auto want = decode_png(want_png, kW, kH);
  REQUIRE(want.size() == rgba.size());
  std::size_t off = 0;
  for (std::size_t i = 0; i < rgba.size(); i += 4) {
    for (std::size_t c = 0; c < 3; ++c) {
      if (std::abs(int{rgba[i + c]} - int{want[i + c]}) > 3) {
        ++off;
        break;
      }
    }
  }
  if (off * 200 > rgba.size() / 4) {
    std::ofstream(name + ".actual.png", std::ios::binary)
        .write(reinterpret_cast<const char*>(png.data()), static_cast<std::streamsize>(png.size()));
  }
  INFO(name << ": " << off << " pixels differ from " << file.string() << " (CADENCE_UPDATE_GOLDEN=1 after an intended change)");
  CHECK(off * 200 <= rgba.size() / 4);
}

viz::RendererConfig small() { return viz::RendererConfig{.width = kW, .height = kH, .budget_ms = 1e9}; }

}  // namespace

TEST_CASE("golden: every shader preset from the fixed pcm") {
  REQUIRE(context());
  viz::Renderer r(small());
  for (auto& s : viz::load_shader_presets(CADENCE_VIZ_PRESETS)) r.add(std::move(s));
  REQUIRE(r.init());
  r.set_auto(false);
  const auto ids = r.ids();
  REQUIRE(ids.size() >= 3);
  for (const auto& id : ids) {
    REQUIRE(r.show(id));
    viz::AudioFeatures f;
    viz::FrameInputs in;
    std::vector<float> slice;
    for (int k = 0; k < 45; ++k) {
      feed(f, in, k, slice);
      r.render(in, in.time_s);
    }
    golden(id.substr(id.find(':') + 1), read_rgba(r.last_fbo()));
  }
  r.release();
}

TEST_CASE("golden: a transition halfway between two presets") {
  REQUIRE(context());
  viz::Renderer r(viz::RendererConfig{.width = kW, .height = kH, .budget_ms = 1e9,
                                      .schedule = {.every_s = 1000, .max_wait_s = 1000, .transition_s = 2}});
  for (auto& s : viz::load_shader_presets(CADENCE_VIZ_PRESETS)) r.add(std::move(s));
  REQUIRE(r.init());
  const std::string first = r.current();
  viz::AudioFeatures f;
  viz::FrameInputs in;
  std::vector<float> slice;
  feed(f, in, 0, slice);
  r.render(in, 0);
  r.next();
  feed(f, in, 1, slice);
  r.render(in, 10.0);
  CHECK(r.current() != first);
  feed(f, in, 2, slice);
  r.render(in, 11.0);
  golden("transition-crossfade", read_rgba(r.last_fbo()));
  // done: the incoming source alone
  feed(f, in, 3, slice);
  r.render(in, 12.5);
  r.release();
}

TEST_CASE("the classic source draws its bars on the ink") {
  REQUIRE(context());
  viz::Renderer r(small());
  r.add(std::make_unique<viz::ClassicSource>(viz::Style::Bars));
  REQUIRE(r.init());
  viz::AudioFeatures f;
  viz::FrameInputs in;
  std::vector<float> slice;
  for (int k = 0; k < 10; ++k) {
    feed(f, in, k, slice);
    r.render(in, in.time_s);
  }
  const auto px = read_rgba(r.last_fbo());
  std::size_t lit = 0;
  for (std::size_t i = 0; i < px.size(); i += 4) lit += px[i] > 100 ? 1 : 0;
  CHECK(lit > 500);
  golden("classic-bars-gl", px);
  r.release();
}

TEST_CASE("a preset that does not compile is dropped, the rest stay") {
  REQUIRE(context());
  std::vector<std::string> events;
  viz::Renderer r(small(), [&](const std::string& e, const std::string& id, double) { events.push_back(e + " " + id); });
  r.add(std::make_unique<viz::ShaderSource>("broken", "void mainImage(out vec4 c, in vec2 p) { c = oops; }"));
  r.add(std::make_unique<viz::ShaderSource>("fine", "void mainImage(out vec4 c, in vec2 p) { c = vec4(fft(0.1)); }"));
  REQUIRE(r.init());
  CHECK(r.ids() == std::vector<std::string>{"shader:fine"});
  CHECK(std::find(events.begin(), events.end(), "viz_source_failed shader:broken") != events.end());
  r.release();
}

namespace {

// a source that costs `ms` a frame
class Slow final : public viz::VizSource {
 public:
  Slow(std::string name, int ms) : name_(std::move(name)), ms_(ms) {}
  [[nodiscard]] std::string id() const override { return "test:" + name_; }
  bool init(int, int) override { return true; }
  void render(const viz::FrameInputs&, unsigned) override {
    std::this_thread::sleep_for(std::chrono::milliseconds(ms_));
    glClearColor(0.5f, 0.5f, 0.5f, 1);
    glClear(GL_COLOR_BUFFER_BIT);
  }

 private:
  std::string name_;
  int ms_;
};

}  // namespace

TEST_CASE("a source over budget for its window is skipped and remembered") {
  REQUIRE(context());
  std::vector<std::string> events;
  viz::Renderer r(viz::RendererConfig{.width = kW, .height = kH, .budget_ms = 10, .budget_window_s = 0.3},
                  [&](const std::string& e, const std::string& id, double) { events.push_back(e + " " + id); });
  r.add(std::make_unique<Slow>("slow", 25));
  r.add(std::make_unique<Slow>("quick", 0));
  REQUIRE(r.init());
  REQUIRE(r.current() == "test:slow");
  viz::FrameInputs in;
  for (int k = 0; k < 20 && r.current() == "test:slow"; ++k) r.render(in, k * 0.1);
  CHECK(r.current() == "test:quick");
  CHECK(std::find(events.begin(), events.end(), "viz_source_slow test:slow") != events.end());
  CHECK(r.costs().at("test:slow").mean_ms >= 20);
  r.release();
}

namespace {

// a source whose start fails, as a milkdrop preset projectM cannot read or compile
class Unstartable final : public viz::VizSource {
 public:
  [[nodiscard]] std::string id() const override { return "test:unstartable"; }
  bool init(int, int) override { return true; }
  bool activate() override { return false; }
  void render(const viz::FrameInputs&, unsigned) override {}
};

}  // namespace

TEST_CASE("a source that fails to start is skipped for the next one, the first included") {
  REQUIRE(context());
  std::vector<std::string> events;
  viz::Renderer r(small(), [&](const std::string& e, const std::string& id, double) { events.push_back(e + " " + id); });
  r.add(std::make_unique<Unstartable>());
  r.add(std::make_unique<Slow>("quick", 0));
  REQUIRE(r.init());
  CHECK(r.current() == "test:quick");
  CHECK(std::find(events.begin(), events.end(), "viz_source_failed test:unstartable") != events.end());
  r.next();
  viz::FrameInputs in;
  for (int k = 0; k < 5; ++k) r.render(in, k * 0.1);
  CHECK(r.current() == "test:quick");
  r.release();
}

TEST_CASE("milkdrop: the shipped list in its order, a name with no file left out") {
  const auto dir = std::filesystem::temp_directory_path() / "cadence-viz-milkdrop-list";
  std::filesystem::remove_all(dir);
  std::filesystem::create_directories(dir / "sub");
  std::ofstream(dir / "b.milk") << "[preset00]\n";
  std::ofstream(dir / "sub" / "a.milk") << "[preset00]\n";
  std::ofstream(dir / "presets.txt") << "# curated\nb.milk\nmissing.milk\nsub/a.milk\n";
  const auto sources = viz::milkdrop_sources(dir.string());
  std::vector<std::string> ids;
  for (const auto& s : sources) ids.push_back(s->id());
  if (viz::milkdrop_built()) {
    CHECK(ids == std::vector<std::string>{"milkdrop:b.milk", "milkdrop:sub/a.milk"});
    std::filesystem::remove(dir / "presets.txt");
    ids.clear();
    for (const auto& s : viz::milkdrop_sources(dir.string())) ids.push_back(s->id());
    CHECK(ids == std::vector<std::string>{"milkdrop:b.milk", "milkdrop:sub/a.milk"});
  } else {
    CHECK(ids.empty());
  }
  CHECK(viz::milkdrop_sources((dir / "none").string()).empty());
  std::filesystem::remove_all(dir);
}

TEST_CASE("milkdrop: a shipped preset draws into the source's fbo from the tap's pcm") {
  const char* dir = std::getenv("CADENCE_VIZ_TEST_MILKDROP");
  if (!viz::milkdrop_built() || dir == nullptr) SKIP("no projectM or no presets (CADENCE_VIZ_TEST_MILKDROP)");
  REQUIRE(context());
  auto sources = viz::milkdrop_sources(dir);
  REQUIRE(sources.size() >= 2);
  sources.resize(2);
  std::vector<std::string> events;
  viz::Renderer r(small(), [&](const std::string& e, const std::string& id, double) { events.push_back(e + " " + id); });
  for (auto& s : sources) r.add(std::move(s));
  REQUIRE(r.init());
  REQUIRE(r.current().rfind("milkdrop:", 0) == 0);
  viz::AudioFeatures f;
  viz::FrameInputs in;
  std::vector<float> slice;
  for (int k = 1; k <= 45; ++k) {
    feed(f, in, k, slice);
    r.render(in, k / 30.0);
  }
  const auto px = read_rgba(r.last_fbo());
  std::size_t lit = 0;
  for (std::size_t i = 0; i < px.size(); i += 4) lit += (px[i] | px[i + 1] | px[i + 2]) > 8 ? 1 : 0;
  INFO(r.current() << ": " << lit << " lit pixels");
  CHECK(lit * 20 > px.size() / 4);
  CHECK(std::find_if(events.begin(), events.end(), [](const std::string& e) {
          return e.rfind("viz_source_failed", 0) == 0;
        }) == events.end());
  r.release();
}

TEST_CASE("golden: every feedback scene from the fixed pcm, trails, palette and bloom included") {
  REQUIRE(context());
  viz::Renderer r(small());
  auto look = std::make_shared<viz::SceneLook>();
  for (auto& s : viz::load_scenes(CADENCE_VIZ_SCENES, look)) r.add(std::move(s));
  REQUIRE(r.init());
  r.set_auto(false);
  const auto ids = r.ids();
  REQUIRE(ids.size() == 7);
  for (const auto& id : ids) {
    REQUIRE(r.show(id));
    viz::AudioFeatures f;
    viz::FrameInputs in;
    std::vector<float> slice;
    for (int k = 0; k < 45; ++k) {
      feed(f, in, k, slice);
      r.render(in, in.time_s);
    }
    const auto px = read_rgba(r.last_fbo());
    // the feedback builds a picture, not a black frame or a white one
    double mean = 0;
    for (std::size_t i = 0; i < px.size(); i += 4) mean += (px[i] + px[i + 1] + px[i + 2]) / 3.0;
    mean /= static_cast<double>(px.size() / 4);
    INFO(id << " mean " << mean);
    CHECK(mean > 3);
    CHECK(mean < 200);
    golden("scene-" + id.substr(id.find(':') + 1), px);
  }
  r.release();
}

TEST_CASE("scenes: no feedback scene strobes over the fixed pcm (photosensitivity), and the tint biases the frame") {
  REQUIRE(context());
  auto look = std::make_shared<viz::SceneLook>();
  viz::Renderer r(small());
  for (auto& s : viz::load_scenes(CADENCE_VIZ_SCENES, look)) r.add(std::move(s));
  REQUIRE(r.init());
  r.set_auto(false);
  for (const auto& id : r.ids()) {
    REQUIRE(r.show(id));
    viz::AudioFeatures f;
    viz::FrameInputs in;
    std::vector<float> slice;
    std::vector<double> luma;
    for (int k = 0; k < 90; ++k) {
      feed(f, in, k, slice);
      r.render(in, in.time_s);
      const auto px = read_rgba(r.last_fbo());
      double l = 0;
      for (std::size_t i = 0; i < px.size(); i += 4) l += (0.2126 * px[i] + 0.7152 * px[i + 1] + 0.0722 * px[i + 2]) / 255.0;
      luma.push_back(l / static_cast<double>(px.size() / 4));
    }
    const auto strobe = viz::check_strobe(luma, 30);
    INFO(id << " flashes/s " << strobe.max_flashes_per_s);
    CHECK_FALSE(strobe.strobing);
  }
  // a red tint at full strength: red gains on blue
  REQUIRE(r.show("scene:nebula"));
  const auto redness = [&] {
    viz::AudioFeatures f;
    viz::FrameInputs in;
    std::vector<float> slice;
    for (int k = 0; k < 20; ++k) {
      feed(f, in, k, slice);
      r.render(in, in.time_s);
    }
    const auto px = read_rgba(r.last_fbo());
    double red = 0, blue = 0;
    for (std::size_t i = 0; i < px.size(); i += 4) {
      red += px[i];
      blue += px[i + 2];
    }
    return red / std::max(blue, 1.0);
  };
  const double plain = redness();
  r.show("scene:vortex");
  look->set_tint(1, 0.2f, 0.2f, 1);
  r.show("scene:nebula");
  CHECK(redness() > plain * 1.5);
  r.release();
}

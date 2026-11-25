// viz-render: one source of the visualizer rendered offline from a track, frame by frame, as raw rgba on stdout, so a
// look can be judged as a video before it reaches a screen (the owner's "make it feel" loop). the host's own path:
// the features from the 1024 frames heard at each frame's time, the renderer, its output read back
//
// usage: viz-render --pcm <f32 stereo 48 khz> --source <id> [--scenes dir] [--art <rgba file> --art-size WxH]
// [--features out.csv: each frame's analysis, for tools/plot-features.py]
//                   [--seconds N] [--start S] [--fps 60] [--size 960x540] [--list]
//   ffmpeg -f f32le -ar 48000 -ac 2 -i song.f32 ... | viz-render ... | ffmpeg -f rawvideo -pix_fmt rgba -s 960x540
//   -r 60 -i - -vf vflip out.mp4 (tools: screen/render/viz/tools/preview.sh)
// a desktop gl 4.1 context on macos (cgl, no window), egl's offscreen gles 3 elsewhere
#include "../classic_source.h"
#include "../drive_source.h"
#include "../features.h"
#include "../renderer.h"
#include "../scene_source.h"

#if defined(__APPLE__)
#include <OpenGL/OpenGL.h>
#else
#include "egl_offscreen.h"
#endif

#include <cstdio>
#include <cstring>
#include <iterator>
#include <cstdlib>
#include <fstream>
#include <limits>
#include <memory>
#include <string>
#include <vector>

namespace viz = cadence::render::viz;

namespace {

struct Options {
  std::string pcm, source, scenes, art, features;
  int width = 960, height = 540, fps = 60, art_w = 0, art_h = 0;
  double seconds = 20, start = 0;
  bool list = false;
};

bool parse(int argc, char** argv, Options& o) {
  for (int i = 1; i < argc; ++i) {
    const std::string a = argv[i];
    auto val = [&]() -> std::string { return i + 1 < argc ? argv[++i] : ""; };
    if (a == "--pcm") o.pcm = val();
    else if (a == "--source") o.source = val();
    else if (a == "--scenes") o.scenes = val();
    else if (a == "--art") o.art = val();
    else if (a == "--features") o.features = val();
    else if (a == "--art-size") {
      if (std::sscanf(val().c_str(), "%dx%d", &o.art_w, &o.art_h) != 2) return false;
    } else if (a == "--seconds") o.seconds = std::atof(val().c_str());
    else if (a == "--start") o.start = std::atof(val().c_str());
    else if (a == "--fps") o.fps = std::atoi(val().c_str());
    else if (a == "--list") o.list = true;
    else if (a == "--size") {
      if (std::sscanf(val().c_str(), "%dx%d", &o.width, &o.height) != 2) return false;
    } else return false;
  }
  return o.list || (!o.pcm.empty() && o.fps > 0 && o.width >= 64 && o.height >= 64);
}

bool offscreen() {
#if defined(__APPLE__)
  const CGLPixelFormatAttribute attrs[] = {kCGLPFAOpenGLProfile, static_cast<CGLPixelFormatAttribute>(kCGLOGLPVersion_3_2_Core),
                                           kCGLPFAAccelerated, kCGLPFAColorSize, static_cast<CGLPixelFormatAttribute>(24),
                                           static_cast<CGLPixelFormatAttribute>(0)};
  CGLPixelFormatObj pf = nullptr;
  GLint n = 0;
  if (CGLChoosePixelFormat(attrs, &pf, &n) != kCGLNoError || pf == nullptr) return false;
  CGLContextObj ctx = nullptr;
  const bool ok = CGLCreateContext(pf, nullptr, &ctx) == kCGLNoError && ctx != nullptr;
  CGLDestroyPixelFormat(pf);
  return ok && CGLSetCurrentContext(ctx) == kCGLNoError;
#else
  return viz::egl_offscreen();
#endif
}

std::vector<std::uint8_t> read_all(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  return {std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
}

}  // namespace

int main(int argc, char** argv) {
  Options o;
  if (!parse(argc, argv, o)) {
    std::fprintf(stderr, "usage: viz-render --pcm file --source id [--scenes dir] [--art rgba --art-size WxH] [--seconds N] "
                         "[--start S] [--fps N] [--size WxH] [--features out.csv] [--list]\n");
    return 2;
  }
  if (!offscreen()) {
    std::fprintf(stderr, "viz-render: no gl context\n");
    return 1;
  }
  viz::RendererConfig rc;
  rc.width = o.width;
  rc.height = o.height;
  rc.budget_ms = std::numeric_limits<double>::max();
  viz::Renderer r(rc, [](const std::string& e, const std::string& id, double) {
    if (e.find("failed") != std::string::npos) std::fprintf(stderr, "viz-render: %s %s\n", e.c_str(), id.c_str());
  });
  auto look = std::make_shared<viz::SceneLook>();
  if (!o.art.empty()) {
    auto px = read_all(o.art);
    if (px.size() == static_cast<std::size_t>(o.art_w) * static_cast<std::size_t>(o.art_h) * 4) {
      look->set_artwork(std::move(px), o.art_w, o.art_h);
    } else {
      std::fprintf(stderr, "viz-render: --art is not %dx%d rgba\n", o.art_w, o.art_h);
    }
  }
  look->set_tint(0.45f, 0.30f, 0.75f, 0.0f);
  if (!o.scenes.empty()) {
    for (auto& s : viz::load_scenes(o.scenes, look)) r.add(std::move(s));
  } else {
    for (auto& s : viz::embedded_scenes(look)) r.add(std::move(s));
  }
  for (auto& s : viz::drive_sources(look)) r.add(std::move(s));
  for (auto st : {viz::Style::Bars, viz::Style::Mirror, viz::Style::Scope}) r.add(std::make_unique<viz::ClassicSource>(st));
  if (!r.init()) {
    std::fprintf(stderr, "viz-render: no source\n");
    return 1;
  }
  if (o.list) {
    for (const auto& id : r.ids()) std::printf("%s\n", id.c_str());
    return 0;
  }
  r.set_auto(o.source.empty());
  if (!o.source.empty() && !r.show(o.source)) {
    std::fprintf(stderr, "viz-render: no source %s\n", o.source.c_str());
    return 1;
  }
  auto bytes = read_all(o.pcm);
  std::vector<float> pcm(bytes.size() / sizeof(float));
  std::memcpy(pcm.data(), bytes.data(), pcm.size() * sizeof(float));
  const std::size_t total = pcm.size() / 2;
  const auto n = static_cast<long>(o.seconds * o.fps);
  std::vector<float> mono(1024), slice;
  std::vector<std::uint8_t> px(static_cast<std::size_t>(o.width) * static_cast<std::size_t>(o.height) * 4);
  viz::AudioFeatures features;
  viz::FrameInputs in;
  std::size_t last = 0;
  std::vector<float> grid_in;
  std::FILE* csv = o.features.empty() ? nullptr : std::fopen(o.features.c_str(), "w");
  if (csv != nullptr)
    std::fprintf(csv,
                 "t,energy,energy_slow,bass,mid,treble,kick,kick_pulse,beat,bpm,phase,bass_presence,build,intensity,drop_pulse,drop_level,"
                 "brightness,spec_hit,grid_beat,drop\n");
  const auto first = static_cast<std::size_t>(o.start * 48000);
  for (long f = 0; f < n; ++f) {
    const double t = o.start + static_cast<double>(f) / o.fps;
    const auto heard = static_cast<std::size_t>(t * 48000);
    if (heard >= total) break;
    for (std::size_t i = 0; i < 1024; ++i) {
      const std::size_t at = heard + i >= 1024 ? heard + i - 1024 : 0;
      mono[i] = heard + i >= 1024 ? 0.5f * (pcm[at * 2] + pcm[at * 2 + 1]) : 0.0f;
    }
    const std::size_t from = std::max(last, heard > 1024 ? heard - 1024 : 0);
    slice.assign(pcm.begin() + static_cast<std::ptrdiff_t>(from * 2), pcm.begin() + static_cast<std::ptrdiff_t>(heard * 2));
    last = heard;
    in.pcm_stereo = slice;
    in.time_s = t;
    // the beat grid fed 150 ms ahead, as the screen's timeline holds at least that much (host.cpp)
    {
      const auto to = std::min<std::size_t>(total, heard + 7200);
      grid_in.clear();
      for (auto i = static_cast<std::size_t>(features.fed()) + first; i < to; ++i)
        grid_in.push_back(0.5f * (pcm[i * 2] + pcm[i * 2 + 1]));
      features.feed(grid_in);
    }
    features.update(mono, 48000, 1.0 / o.fps, in, static_cast<std::int64_t>(heard - first));
    r.render(in, t);
    if (csv != nullptr)
      std::fprintf(csv, "%.4f,%.4f,%.4f,%.4f,%.4f,%.4f,%d,%.4f,%d,%.2f,%.4f,%.4f,%.4f,%.4f,%.4f,%.4f,%.4f,%.4f,%.4f,%.4f\n", t, in.energy, in.energy_slow,
                   in.bass_level, in.mid_level, in.treble_level, in.kick ? 1 : 0, in.kick_pulse, in.beat ? 1 : 0, in.bpm,
                   in.beat_phase, in.bass_presence, in.build, in.intensity, in.drop_pulse, in.drop_level, in.brightness,
                   in.spec_strength, in.grid_strength, in.drop ? in.drop_pulse : 0.0f);
    glBindFramebuffer(GL_READ_FRAMEBUFFER, r.last_fbo());
    glReadPixels(0, 0, o.width, o.height, GL_RGBA, GL_UNSIGNED_BYTE, px.data());
    if (std::fwrite(px.data(), 1, px.size(), stdout) != px.size()) return 1;
    if (f % (o.fps * 5) == 0) std::fprintf(stderr, "viz-render: %.0f s, %s, bpm %.0f\n", t, r.current().c_str(), in.bpm);
  }
  r.release();
  if (csv != nullptr) std::fclose(csv);
  return 0;
}

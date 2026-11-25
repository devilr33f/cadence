// viz-bench: the visualizer's curation tool. renders each source alone for --seconds as the host does (gles 3, the
// internal size, a glFinish per frame) from a fixed beat (or --pcm, f32 stereo 48 kHz) and prints a table ranked
// cheapest first, one tab-separated row each: id, median_ms, p95_ms, frames, strobe, source. strobe is the
// photosensitivity check of the frames' mean luminance (more than 3 flashes in a second fails). the shipped lists
// keep what the stick renders within its budget: media/third_party/projectm/rank-presets.sh reads this table.
//
// usage: viz-bench [--shaders <dir>] [--scenes <dir>] [--milkdrop <preset-dir> --list <names>] [--classic] [--seconds 10]
//                  [--fps 30] [--size 960x540] [--pcm <file>] [--no-luma] [--source <label>]
// on the stick (owner-attended): built with the ndk, pushed with libprojectM-4.so to /data/local/tmp and run from
// adb shell; it needs no window
#include "../classic_source.h"
#include "../features.h"
#include "../renderer.h"
#include "../scene_source.h"
#include "../shader_source.h"
#include "../strobe.h"
#include "egl_offscreen.h"
#ifdef CADENCE_VIZ_PROJECTM
#include "../../milkdrop/milkdrop_source.h"
#include "../playlist.h"
#endif

#include <algorithm>
#include <array>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <limits>
#include <numbers>
#include <string>
#include <vector>

namespace viz = cadence::render::viz;

namespace {

struct Options {
  std::string shaders, scenes, milkdrop, list, pcm, source = "host";
  bool classic = false, luma = true;
  int width = 960, height = 540, fps = 30;
  double seconds = 10;
};

std::vector<float> synthetic(int seconds) {
  std::vector<float> out;
  for (int i = 0; i < seconds * 48000; ++i) {
    const double t = i / 48000.0;
    const double beat = std::exp(-12 * std::fmod(t, 0.5));
    const double s = 0.5 * beat * std::sin(2 * std::numbers::pi * 60 * t) +
                     0.2 * std::sin(2 * std::numbers::pi * 220 * t) + 0.1 * std::sin(2 * std::numbers::pi * 1320 * t);
    out.push_back(static_cast<float>(s));
    out.push_back(static_cast<float>(s * 0.8));
  }
  return out;
}

double percentile(std::vector<double> v, double p) {
  if (v.empty()) return 0;
  std::sort(v.begin(), v.end());
  return v[static_cast<std::size_t>(std::lround(p * static_cast<double>(v.size() - 1)))];
}

bool parse(int argc, char** argv, Options& o) {
  for (int i = 1; i < argc; ++i) {
    const std::string a = argv[i];
    auto val = [&]() -> std::string { return i + 1 < argc ? argv[++i] : ""; };
    if (a == "--shaders") o.shaders = val();
    else if (a == "--milkdrop") o.milkdrop = val();
    else if (a == "--list") o.list = val();
    else if (a == "--classic") o.classic = true;
    else if (a == "--scenes") o.scenes = val();
    else if (a == "--pcm") o.pcm = val();
    else if (a == "--source") o.source = val();
    else if (a == "--seconds") o.seconds = std::atof(val().c_str());
    else if (a == "--fps") o.fps = std::atoi(val().c_str());
    else if (a == "--no-luma") o.luma = false;
    else if (a == "--size") {
      if (std::sscanf(val().c_str(), "%dx%d", &o.width, &o.height) != 2) return false;
    } else return false;
  }
  return o.seconds > 0 && o.fps > 0 && o.width >= 64 && o.height >= 64;
}

}  // namespace

int main(int argc, char** argv) {
  Options o;
  if (!parse(argc, argv, o)) {
    std::fprintf(stderr, "usage: viz-bench [--shaders dir] [--milkdrop dir --list names] [--classic] [--scenes dir] [--seconds N] "
                         "[--fps N] [--size WxH] [--pcm file] [--no-luma] [--source label]\n");
    return 2;
  }
  if (!viz::egl_offscreen()) {
    std::fprintf(stderr, "viz-bench: no egl gles 3 context\n");
    return 1;
  }
  // no budget, no auto switching: every source runs its whole time
  viz::RendererConfig rc;
  rc.width = o.width;
  rc.height = o.height;
  rc.budget_ms = std::numeric_limits<double>::max();
  viz::Renderer r(rc, [](const std::string& e, const std::string& id, double) {
    if (e.find("failed") != std::string::npos) std::fprintf(stderr, "viz-bench: %s %s\n", e.c_str(), id.c_str());
  });
  if (!o.shaders.empty()) {
    for (auto& s : viz::load_shader_presets(o.shaders)) r.add(std::move(s));
  }
  if (!o.scenes.empty()) {
    for (auto& s : viz::load_scenes(o.scenes, nullptr)) r.add(std::move(s));
  }
  if (o.classic) {
    for (auto st : {viz::Style::Bars, viz::Style::Mirror, viz::Style::Scope}) {
      r.add(std::make_unique<viz::ClassicSource>(st));
    }
  }
#ifdef CADENCE_VIZ_PROJECTM
  if (!o.milkdrop.empty()) {
    for (const auto& name : viz::read_list(o.list)) r.add(std::make_unique<viz::MilkdropSource>(o.milkdrop, name));
  }
#endif
  if (!r.init()) {
    std::fprintf(stderr, "viz-bench: no source\n");
    return 1;
  }
  r.set_auto(false);

  std::vector<float> pcm;
  if (!o.pcm.empty()) {
    std::ifstream in(o.pcm, std::ios::binary);
    in.seekg(0, std::ios::end);
    pcm.resize(static_cast<std::size_t>(in.tellg()) / sizeof(float));
    in.seekg(0);
    in.read(reinterpret_cast<char*>(pcm.data()), static_cast<std::streamsize>(pcm.size() * sizeof(float)));
  }
  if (pcm.size() < 96000) pcm = synthetic(30);
  const std::size_t frames_total = pcm.size() / 2;
  const auto per_frame = static_cast<std::size_t>(48000 / o.fps);
  const auto n = static_cast<int>(o.seconds * o.fps);

  std::array<double, 256> lin{};
  for (std::size_t i = 0; i < 256; ++i) {
    const double c = static_cast<double>(i) / 255.0;
    lin[i] = c <= 0.04045 ? c / 12.92 : std::pow((c + 0.055) / 1.055, 2.4);
  }
  std::vector<std::uint8_t> px(static_cast<std::size_t>(o.width) * static_cast<std::size_t>(o.height) * 4);

  struct Row {
    std::string id;
    double median, p95;
    std::size_t frames;
    std::string strobe;
  };
  std::vector<Row> rows;
  for (const auto& id : r.ids()) {
    if (!r.show(id)) continue;
    viz::AudioFeatures features;
    viz::FrameInputs in;
    std::vector<float> mono(1024), slice;
    std::vector<double> ms, luma;
    std::size_t pos = 1024;
    for (int f = 0; f < n; ++f) {
      if (pos + per_frame > frames_total) pos = 1024;
      slice.assign(pcm.begin() + static_cast<std::ptrdiff_t>(pos * 2),
                   pcm.begin() + static_cast<std::ptrdiff_t>((pos + per_frame) * 2));
      pos += per_frame;
      for (std::size_t i = 0; i < 1024; ++i) {
        const std::size_t at = pos - 1024 + i;
        mono[i] = 0.5f * (pcm[at * 2] + pcm[at * 2 + 1]);
      }
      in.pcm_stereo = slice;
      in.time_s = f / static_cast<double>(o.fps);
      features.update(mono, 48000, 1.0 / o.fps, in);
      r.render(in, in.time_s);
      ms.push_back(r.costs().at(id).last_ms);
      if (o.luma) {
        glBindFramebuffer(GL_READ_FRAMEBUFFER, r.last_fbo());
        glReadPixels(0, 0, o.width, o.height, GL_RGBA, GL_UNSIGNED_BYTE, px.data());
        double sum = 0;
        std::size_t k = 0;
        for (std::size_t p = 0; p < px.size(); p += 16) {
          sum += 0.2126 * lin[px[p]] + 0.7152 * lin[px[p + 1]] + 0.0722 * lin[px[p + 2]];
          ++k;
        }
        luma.push_back(sum / static_cast<double>(k));
      }
    }
    std::string strobe = "unchecked";
    if (o.luma) strobe = viz::check_strobe(luma, o.fps).strobing ? "strobe" : "ok";
    rows.push_back({id, percentile(ms, 0.5), percentile(ms, 0.95), ms.size(), strobe});
  }
  std::sort(rows.begin(), rows.end(), [](const Row& a, const Row& b) { return a.median < b.median; });
  std::printf("id\tmedian_ms\tp95_ms\tframes\tstrobe\tsource\n");
  for (const auto& row : rows) {
    std::printf("%s\t%.2f\t%.2f\t%zu\t%s\t%s\n", row.id.c_str(), row.median, row.p95, row.frames, row.strobe.c_str(),
                o.source.c_str());
  }
  r.release();
  return 0;
}

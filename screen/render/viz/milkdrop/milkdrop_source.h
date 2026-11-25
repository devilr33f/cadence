#pragma once

// a milkdrop preset through libprojectM 4 (lgpl-2.1, linked as a shared library; media/third_party/projectm builds it
// with the fbo backport) as one of the host's sources. it renders on the viz thread like every source (never the
// ui's: S5's projectM starved the ui loop and slowed the video), and its projectM instance lives only while it is
// shown, so at most two exist during a transition. S5 on the stick: 14 ms to 870 ms a frame by preset, so the
// host's budget skips the slow ones and the benchmark curates the list
#include "../host/source.h"

#include <string>

namespace cadence::render::viz {

struct MilkdropConfig {
  int mesh_x = 48;
  int mesh_y = 32;
  int fps = 30;
};

class MilkdropSource final : public VizSource {
 public:
  // preset is a path under preset_dir (a presets.txt name). init drops a name with no file; activate fails (and the
  // renderer skips it) when projectM cannot read or compile it
  MilkdropSource(std::string preset_dir, std::string preset, MilkdropConfig c = {});
  ~MilkdropSource() override;

  [[nodiscard]] std::string id() const override { return "milkdrop:" + preset_; }
  bool init(int width, int height) override;
  bool activate() override;
  void render(const FrameInputs& in, unsigned fbo) override;
  void deactivate() override;
  void release() override { deactivate(); }

 private:
  static void restore_state();

  std::string dir_, preset_;
  MilkdropConfig c_;
  int w_ = 0, h_ = 0;
  void* pm_ = nullptr;
};

}  // namespace cadence::render::viz

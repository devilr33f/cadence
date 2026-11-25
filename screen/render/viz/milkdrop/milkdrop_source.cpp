#include "milkdrop_source.h"

#include "../host/gl.h"

#include <projectM-4/projectM.h>

#include <filesystem>
#include <system_error>

namespace cadence::render::viz {

namespace {

// projectM reports a preset it cannot read or compile through this callback alone (its load returns nothing and the
// idle preset stays on), so the source asks for it to know the load failed
void on_switch_failed(const char*, const char*, void* user_data) { *static_cast<bool*>(user_data) = true; }

}  // namespace

MilkdropSource::MilkdropSource(std::string preset_dir, std::string preset, MilkdropConfig c)
    : dir_(std::move(preset_dir)), preset_(std::move(preset)), c_(c) {}

MilkdropSource::~MilkdropSource() { deactivate(); }

bool MilkdropSource::init(int width, int height) {
  w_ = width;
  h_ = height;
  if (preset_.empty() || width <= 0 || height <= 0) return false;
  // a name the pack lacks is dropped here, never shown as projectM's idle preset
  std::error_code ec;
  return std::filesystem::is_regular_file(std::filesystem::path(dir_) / preset_, ec);
}

bool MilkdropSource::activate() {
  if (pm_) return true;
  projectm_handle pm = projectm_create();
  if (!pm) return false;
  projectm_set_window_size(pm, static_cast<std::size_t>(w_), static_cast<std::size_t>(h_));
  projectm_set_fps(pm, c_.fps);
  projectm_set_mesh_size(pm, static_cast<std::size_t>(c_.mesh_x), static_cast<std::size_t>(c_.mesh_y));
  // the host chooses and blends sources: never projectM's own timer or blend
  projectm_set_preset_duration(pm, 1e9);
  projectm_set_preset_locked(pm, true);
  projectm_set_aspect_correction(pm, true);
  // a preset's own textures beside it or in the pack's textures/; projectM's built-in noise needs none. set before the
  // load, as it rebuilds the texture manager
  const std::string textures = dir_ + "/textures";
  const char* paths[] = {dir_.c_str(), textures.c_str()};
  projectm_set_texture_search_paths(pm, paths, 2);
  bool failed = false;
  projectm_set_preset_switch_failed_event_callback(pm, on_switch_failed, &failed);
  projectm_load_preset_file(pm, (dir_ + "/" + preset_).c_str(), false);
  projectm_set_preset_switch_failed_event_callback(pm, nullptr, nullptr);
  if (failed) {
    // the renderer logs viz_source_failed and stays on what it showed
    projectm_destroy(pm);
    restore_state();
    return false;
  }
  pm_ = pm;
  restore_state();
  return true;
}

void MilkdropSource::render(const FrameInputs& in, unsigned fbo) {
  auto* pm = static_cast<projectm_handle>(pm_);
  if (!pm) return;
  // the frames heard since the last frame, interleaved stereo; projectM keeps the newest 576 for its own analysis
  if (in.pcm_stereo.size() >= 2) {
    projectm_pcm_add_float(pm, in.pcm_stereo.data(), static_cast<unsigned int>(in.pcm_stereo.size() / 2),
                           PROJECTM_STEREO);
  }
  projectm_opengl_render_frame_fbo(pm, fbo);
  restore_state();
  glBindFramebuffer(GL_FRAMEBUFFER, fbo);
}

void MilkdropSource::deactivate() {
  if (pm_) {
    projectm_destroy(static_cast<projectm_handle>(pm_));
    restore_state();
  }
  pm_ = nullptr;
}

// projectM leaves its own program, vertex array, textures and blend bound; the host's stages set what they use, but a
// stale blend or vao must never leak into the next source or the transition
void MilkdropSource::restore_state() {
  glDisable(GL_BLEND);
  glDisable(GL_DEPTH_TEST);
  glDisable(GL_SCISSOR_TEST);
  glUseProgram(0);
  glBindVertexArray(0);
  glBindBuffer(GL_ARRAY_BUFFER, 0);
  glActiveTexture(GL_TEXTURE0);
  glBindTexture(GL_TEXTURE_2D, 0);
  glPixelStorei(GL_UNPACK_ALIGNMENT, 4);
}

}  // namespace cadence::render::viz

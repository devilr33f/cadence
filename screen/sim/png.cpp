#include "sim/png.h"

#if defined(__GNUC__)
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wconversion"
#pragma GCC diagnostic ignored "-Wsign-conversion"
#pragma GCC diagnostic ignored "-Wshadow"
#pragma GCC diagnostic ignored "-Wmissing-field-initializers"
#endif
// the writer's implementation lives here; the reader's is the ui's (rml/renderer.cpp), and the smoke test brings
// its own beside a sim built without the ui
#define STB_IMAGE_WRITE_IMPLEMENTATION
#include <stb/stb_image_write.h>
#define STBI_NO_STDIO
#include <stb/stb_image.h>
#if defined(__GNUC__)
#pragma GCC diagnostic pop
#endif

#include <fstream>
#include <iterator>

namespace cadence::screen::sim {

bool writePng(const std::string& path, const render::Image& image) {
  if (image.rgba.empty()) return false;
  return stbi_write_png(path.c_str(), image.width, image.height, 4, image.rgba.data(),
                        static_cast<int>(image.width) * 4) != 0;
}

bool readPng(const std::string& path, render::Image& image) {
  std::ifstream in(path, std::ios::binary);
  if (!in) return false;
  const std::string bytes{std::istreambuf_iterator<char>(in), {}};
  int w = 0, h = 0, n = 0;
  auto* data = stbi_load_from_memory(reinterpret_cast<const stbi_uc*>(bytes.data()),
                                      static_cast<int>(bytes.size()), &w, &h, &n, 4);
  if (data == nullptr) return false;
  image.width = w;
  image.height = h;
  image.rgba.assign(data, data + static_cast<std::size_t>(w) * static_cast<std::size_t>(h) * 4);
  stbi_image_free(data);
  return true;
}

}  // namespace cadence::screen::sim

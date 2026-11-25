#include "milkdrop_sources.h"

#include "../host/playlist.h"

#include <algorithm>
#include <cstdlib>
#include <filesystem>
#include <system_error>

namespace cadence::render::viz {

namespace fs = std::filesystem;

namespace {

bool is_dir(const std::string& p) {
  std::error_code ec;
  return !p.empty() && fs::is_directory(p, ec);
}

std::string env(const char* name) {
  const char* v = std::getenv(name);
  return v != nullptr ? std::string(v) : std::string();
}

}  // namespace

bool milkdrop_built() {
#ifdef CADENCE_VIZ_PROJECTM
  return true;
#else
  return false;
#endif
}

std::string milkdrop_dir() {
  if (std::string d = env("CADENCE_SCREEN_MILKDROP"); !d.empty()) return is_dir(d) ? d : std::string();
  if (std::string ui = env("CADENCE_SCREEN_UI_ASSETS"); !ui.empty()) {
    ui = ui.substr(0, ui.find(':'));
    while (ui.size() > 1 && ui.back() == '/') ui.pop_back();
    const std::string d = (fs::path(ui).parent_path() / "viz" / "milkdrop").string();
    if (is_dir(d)) return d;
  }
#ifdef CADENCE_VIZ_MILKDROP_DIR
  if (is_dir(CADENCE_VIZ_MILKDROP_DIR)) return CADENCE_VIZ_MILKDROP_DIR;
#endif
  return {};
}

std::vector<std::unique_ptr<VizSource>> milkdrop_sources(const std::string& dir, MilkdropConfig c) {
  std::vector<std::unique_ptr<VizSource>> out;
#ifdef CADENCE_VIZ_PROJECTM
  if (!is_dir(dir)) return out;
  std::vector<std::string> names;
  std::error_code ec;
  if (const fs::path list = fs::path(dir) / "presets.txt"; fs::is_regular_file(list, ec)) {
    names = read_list(list.string());
  } else {
    for (auto it = fs::recursive_directory_iterator(dir, ec); !ec && it != fs::recursive_directory_iterator();
         it.increment(ec)) {
      if (it->is_regular_file(ec) && it->path().extension() == ".milk") {
        names.push_back(fs::relative(it->path(), dir, ec).generic_string());
      }
    }
    std::sort(names.begin(), names.end());
  }
  for (const auto& n : names) {
    if (!fs::is_regular_file(fs::path(dir) / n, ec)) continue;
    out.push_back(std::make_unique<MilkdropSource>(dir, n, c));
  }
#else
  (void)dir;
  (void)c;
#endif
  return out;
}

}  // namespace cadence::render::viz

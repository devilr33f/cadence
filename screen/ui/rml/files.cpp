#include "ui/rml/files.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <sstream>

namespace cadence::screen::ui {

namespace {

bool safe(const std::string& path) {
  if (path.empty() || path[0] == '/') return false;
  std::size_t start = 0;
  while (start <= path.size()) {
    const auto end = path.find('/', start);
    const auto part = path.substr(start, end == std::string::npos ? std::string::npos : end - start);
    if (part == "..") return false;
    if (end == std::string::npos) break;
    start = end + 1;
  }
  return true;
}

}  // namespace

bool RmlFiles::read(const std::string& rawPath, std::string& out) {
  std::string path = rawPath;
  // rmlui joins a document's relative paths to the document's own path, which for a memory document is "mem:..."
  if (const auto m = memory_.find(path); m != memory_.end()) {
    out = m->second;
    return true;
  }
  while (path.starts_with("./")) path = path.substr(2);
  if (!safe(path)) return false;
  for (const auto& root : roots_) {
    std::ifstream in(root + "/" + path, std::ios::binary);
    if (!in) continue;
    std::ostringstream s;
    s << in.rdbuf();
    out = s.str();
    return true;
  }
  return false;
}

Rml::FileHandle RmlFiles::Open(const Rml::String& path) {
  auto* f = new Open_;
  if (!read(path, f->data)) {
    delete f;
    return 0;
  }
  return reinterpret_cast<Rml::FileHandle>(f);
}

void RmlFiles::Close(Rml::FileHandle file) { delete reinterpret_cast<Open_*>(file); }

size_t RmlFiles::Read(void* buffer, size_t size, Rml::FileHandle file) {
  auto* f = reinterpret_cast<Open_*>(file);
  const size_t n = std::min(size, f->data.size() - f->pos);
  std::memcpy(buffer, f->data.data() + f->pos, n);
  f->pos += n;
  return n;
}

bool RmlFiles::Seek(Rml::FileHandle file, long offset, int origin) {
  auto* f = reinterpret_cast<Open_*>(file);
  long base = 0;
  if (origin == SEEK_CUR) base = static_cast<long>(f->pos);
  if (origin == SEEK_END) base = static_cast<long>(f->data.size());
  const long pos = base + offset;
  if (pos < 0 || pos > static_cast<long>(f->data.size())) return false;
  f->pos = static_cast<size_t>(pos);
  return true;
}

size_t RmlFiles::Tell(Rml::FileHandle file) { return reinterpret_cast<Open_*>(file)->pos; }

size_t RmlFiles::Length(Rml::FileHandle file) { return reinterpret_cast<Open_*>(file)->data.size(); }

std::vector<std::string> defaultAssetRoots() {
  std::vector<std::string> roots;
  if (const char* env = std::getenv("CADENCE_SCREEN_UI_ASSETS"); env != nullptr && *env != '\0') {
    std::string s = env;
    std::size_t start = 0;
    while (start <= s.size()) {
      const auto end = s.find(':', start);
      roots.push_back(s.substr(start, end == std::string::npos ? std::string::npos : end - start));
      if (end == std::string::npos) break;
      start = end + 1;
    }
    return roots;
  }
  roots.emplace_back(CADENCE_SCREEN_UI_GENERATED);
  roots.emplace_back(CADENCE_SCREEN_UI_ASSETS);
  return roots;
}

}  // namespace cadence::screen::ui

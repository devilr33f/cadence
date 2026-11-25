#pragma once

#include <RmlUi/Core/FileInterface.h>

#include <map>
#include <string>
#include <vector>

namespace cadence::screen::ui {

// rmlui's file interface over the ui's asset roots (the source assets and the build's generated rcss), searched in
// order, plus documents registered in memory ("mem:name"). paths are relative; one that climbs out (..) or is
// absolute opens nothing
class RmlFiles final : public Rml::FileInterface {
 public:
  explicit RmlFiles(std::vector<std::string> roots) : roots_(std::move(roots)) {}

  void addMemory(const std::string& name, std::string data) { memory_[name] = std::move(data); }
  // the file's bytes, or false
  bool read(const std::string& path, std::string& out);

  Rml::FileHandle Open(const Rml::String& path) override;
  void Close(Rml::FileHandle file) override;
  size_t Read(void* buffer, size_t size, Rml::FileHandle file) override;
  bool Seek(Rml::FileHandle file, long offset, int origin) override;
  size_t Tell(Rml::FileHandle file) override;
  size_t Length(Rml::FileHandle file) override;

 private:
  struct Open_ {
    std::string data;
    size_t pos = 0;
  };
  std::vector<std::string> roots_;
  std::map<std::string, std::string> memory_;
};

// the asset roots this build knows: $CADENCE_SCREEN_UI_ASSETS (colon separated) when set, else the source and
// generated directories the build was configured with
std::vector<std::string> defaultAssetRoots();

}  // namespace cadence::screen::ui

#pragma once

#include <atomic>
#include <chrono>
#include <filesystem>
#include <string>

namespace cadence::test_support {

// a fresh directory removed with the object
class TempDir {
 public:
  TempDir() {
    static std::atomic<int> counter{0};
    const auto stamp = std::chrono::steady_clock::now().time_since_epoch().count();
    path_ = std::filesystem::temp_directory_path() / ("cadence-media-" + std::to_string(stamp) + "-" + std::to_string(counter++));
    std::filesystem::create_directories(path_);
  }
  ~TempDir() {
    std::error_code ignored;
    std::filesystem::remove_all(path_, ignored);
  }
  TempDir(const TempDir&) = delete;
  TempDir& operator=(const TempDir&) = delete;

  [[nodiscard]] std::string str() const { return path_.string(); }
  [[nodiscard]] std::string file(const std::string& name) const { return (path_ / name).string(); }

 private:
  std::filesystem::path path_;
};

}  // namespace cadence::test_support

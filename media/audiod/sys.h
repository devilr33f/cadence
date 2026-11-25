#pragma once

#include <unistd.h>

#include <string_view>
#include <utility>

namespace cadence::audiod {

// a failed system call: its name and errno
struct SysError {
  std::string_view call;
  int code = 0;
};

// owns one file descriptor and closes it once
class UniqueFd {
 public:
  UniqueFd() = default;
  explicit UniqueFd(int fd) noexcept : fd_(fd) {}
  ~UniqueFd() { reset(); }

  UniqueFd(UniqueFd&& other) noexcept : fd_(other.release()) {}
  UniqueFd& operator=(UniqueFd&& other) noexcept {
    if (this != &other)
      reset(other.release());
    return *this;
  }
  UniqueFd(const UniqueFd&) = delete;
  UniqueFd& operator=(const UniqueFd&) = delete;

  [[nodiscard]] int get() const noexcept { return fd_; }
  [[nodiscard]] bool valid() const noexcept { return fd_ >= 0; }
  explicit operator bool() const noexcept { return valid(); }

  int release() noexcept { return std::exchange(fd_, -1); }
  void reset(int fd = -1) noexcept {
    if (fd_ >= 0)
      ::close(fd_);
    fd_ = fd;
  }

 private:
  int fd_ = -1;
};

}  // namespace cadence::audiod

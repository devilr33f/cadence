#include "audiod/airplay-files.h"

#include "audiod/fs-tree.h"

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cerrno>
#include <filesystem>
#include <system_error>

namespace cadence::audiod::airplay {

namespace {

SysError failed(std::string_view call, int code = errno) { return {.call = call, .code = code}; }

// the parent's descriptor and the last component of an absolute path; errno says why not
struct Split {
  UniqueFd parent;
  std::string name;
};

std::optional<Split> split(const std::string& path) {
  const auto slash = path.rfind('/');
  auto name = slash == std::string::npos ? std::string() : path.substr(slash + 1);
  if (!path.starts_with('/') || name.empty() || name == "." || name == "..") {
    errno = EINVAL;
    return std::nullopt;
  }
  UniqueFd parent(::open(slash == 0 ? "/" : path.substr(0, slash).c_str(), O_RDONLY | O_DIRECTORY | O_CLOEXEC));
  if (!parent)
    return std::nullopt;
  return Split{.parent = std::move(parent), .name = std::move(name)};
}

// the directory at path opened, never through a symlink, ours and made 0700
std::optional<SysError> own(const std::string& path) {
  const UniqueFd dir(::open(path.c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
  if (!dir)
    return failed("open");
  struct stat info {};
  if (::fstat(dir.get(), &info) != 0)
    return failed("fstat");
  if (info.st_uid != ::geteuid())
    return failed("owner", EPERM);
  if (::fchmod(dir.get(), 0700) != 0)
    return failed("fchmod");
  return std::nullopt;
}

}  // namespace

std::optional<SysError> privateDir(const std::string& path) {
  struct stat info {};
  if (::lstat(path.c_str(), &info) == 0) {
    if (S_ISLNK(info.st_mode))
      return failed("lstat", ELOOP);
    if (!S_ISDIR(info.st_mode))
      return failed("lstat", ENOTDIR);
    return own(path);
  }
  if (errno != ENOENT)
    return failed("lstat");
  std::error_code error;
  std::filesystem::create_directories(std::filesystem::path(path).parent_path(), error);
  if (error)
    return failed("mkdir", error.value());
  if (::mkdir(path.c_str(), 0700) != 0 && errno != EEXIST)
    return failed("mkdir");
  return own(path);
}

std::optional<SysError> freshDir(const std::string& path) {
  auto parts = split(path);
  if (!parts)
    return failed("open");
  if (const auto error = removeEntry(parts->parent.get(), parts->name))
    return error;
  if (::mkdirat(parts->parent.get(), parts->name.c_str(), 0700) != 0)
    return failed("mkdirat");
  return own(path);
}

std::optional<SysError> writePrivate(const std::string& path, std::string_view text) {
  // non-blocking, so a fifo planted at the path fails the open instead of holding it
  const UniqueFd file(::open(path.c_str(), O_WRONLY | O_CREAT | O_TRUNC | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC, 0600));
  if (!file)
    return failed("open");
  struct stat info {};
  if (::fstat(file.get(), &info) != 0)
    return failed("fstat");
  if (!S_ISREG(info.st_mode))
    return failed("fstat", EEXIST);
  if (::fchmod(file.get(), 0600) != 0)
    return failed("fchmod");
  while (!text.empty()) {
    const auto written = ::write(file.get(), text.data(), text.size());
    if (written < 0 && errno == EINTR)
      continue;
    if (written <= 0)
      return failed("write");
    text.remove_prefix(static_cast<std::size_t>(written));
  }
  return std::nullopt;
}

std::optional<SysError> removeStale(const std::string& path) {
  struct stat info {};
  if (::lstat(path.c_str(), &info) != 0)
    return errno == ENOENT ? std::nullopt : std::optional(failed("lstat"));
  if (S_ISLNK(info.st_mode))
    return failed("lstat", ELOOP);
  if (S_ISDIR(info.st_mode))
    return failed("lstat", EISDIR);
  if (::unlink(path.c_str()) != 0 && errno != ENOENT)
    return failed("unlink");
  return std::nullopt;
}

std::optional<SysError> privateFifo(const std::string& path) {
  struct stat info {};
  if (::lstat(path.c_str(), &info) != 0) {
    if (errno != ENOENT)
      return failed("lstat");
    if (::mkfifo(path.c_str(), 0600) != 0)
      return failed("mkfifo");
  } else if (S_ISLNK(info.st_mode)) {
    return failed("lstat", ELOOP);
  } else if (!S_ISFIFO(info.st_mode)) {
    return failed("lstat", EEXIST);
  }
  // the read end opens at once with no writer; what it reaches is checked through the descriptor
  const UniqueFd fifo(::open(path.c_str(), O_RDONLY | O_NONBLOCK | O_NOFOLLOW | O_CLOEXEC));
  if (!fifo)
    return failed("open");
  if (::fstat(fifo.get(), &info) != 0)
    return failed("fstat");
  if (!S_ISFIFO(info.st_mode))
    return failed("fstat", EEXIST);
  if (info.st_uid != ::geteuid())
    return failed("owner", EPERM);
  if (::fchmod(fifo.get(), 0600) != 0)
    return failed("fchmod");
  return std::nullopt;
}

}  // namespace cadence::audiod::airplay

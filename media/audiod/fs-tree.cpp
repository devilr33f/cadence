#include "audiod/fs-tree.h"

#include <dirent.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cerrno>
#include <string_view>
#include <vector>

namespace cadence::audiod {

namespace {

std::optional<SysError> failed(std::string_view call) { return SysError{.call = call, .code = errno}; }

std::optional<SysError> names(int dir, std::vector<std::string>& found) {
  UniqueFd copy(::fcntl(dir, F_DUPFD_CLOEXEC, 0));
  if (!copy)
    return failed("fcntl");
  DIR* listing = ::fdopendir(copy.get());
  if (listing == nullptr)
    return failed("fdopendir");
  // the listing owns it now
  copy.release();
  errno = 0;
  while (const dirent* entry = ::readdir(listing)) {
    const std::string_view name = entry->d_name;
    if (name != "." && name != "..")
      found.emplace_back(name);
  }
  const int code = errno;
  ::closedir(listing);
  if (code != 0)
    return SysError{.call = "readdir", .code = code};
  return std::nullopt;
}

}  // namespace

std::optional<SysError> removeEntry(int dir, const std::string& name) {
  struct stat info {};
  if (::fstatat(dir, name.c_str(), &info, AT_SYMLINK_NOFOLLOW) != 0)
    return errno == ENOENT ? std::nullopt : failed("fstatat");
  if (!S_ISDIR(info.st_mode))
    return ::unlinkat(dir, name.c_str(), 0) == 0 ? std::nullopt : failed("unlinkat");
  const UniqueFd inner(::openat(dir, name.c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
  if (!inner)
    return failed("openat");
  ::fchmod(inner.get(), 0700);
  std::vector<std::string> entries;
  if (const auto error = names(inner.get(), entries))
    return error;
  for (const auto& entry : entries)
    if (const auto error = removeEntry(inner.get(), entry))
      return error;
  return ::unlinkat(dir, name.c_str(), AT_REMOVEDIR) == 0 ? std::nullopt : failed("unlinkat");
}

}  // namespace cadence::audiod

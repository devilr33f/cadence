#include "audiod/soloist-scratch.h"

#include "audiod/fs-tree.h"
#include "audiod/sys.h"

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cerrno>
#include <string_view>

namespace cadence::audiod {

namespace {

// nobody: soloist's uid and gid in its jail
constexpr uid_t kNobody = 65534;

std::optional<SysError> failed(std::string_view call) { return SysError{.call = call, .code = errno}; }

// private to its creator until its owner and its final mode are set
std::optional<SysError> makeDir(int dir, const char* name, mode_t mode, uid_t uid, gid_t gid, UniqueFd& made) {
  if (::mkdirat(dir, name, 0700) != 0)
    return failed("mkdirat");
  made.reset(::openat(dir, name, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
  if (!made)
    return failed("openat");
  if (::geteuid() == 0 && ::fchown(made.get(), uid, gid) != 0)
    return failed("fchown");
  if (::fchmod(made.get(), mode) != 0)
    return failed("fchmod");
  return std::nullopt;
}

std::optional<SysError> makeHome(int scratch) {
  UniqueFd home;
  UniqueFd crashpad;
  if (const auto error = makeDir(scratch, "home", 01770, 0, kNobody, home))
    return error;
  return makeDir(home.get(), "crashpad", 0555, 0, 0, crashpad);
}

std::optional<SysError> prepare(const std::string& root) {
  const auto slash = root.rfind('/');
  const auto name = slash == std::string::npos ? std::string() : root.substr(slash + 1);
  if (!root.starts_with('/') || name.empty() || name == "." || name == "..")
    return SysError{.call = "path", .code = 0};
  const UniqueFd parent(::open(slash == 0 ? "/" : root.substr(0, slash).c_str(), O_RDONLY | O_DIRECTORY | O_CLOEXEC));
  if (!parent)
    return failed("open");
  if (const auto error = removeEntry(parent.get(), name))
    return error;
  UniqueFd scratch;
  UniqueFd run;
  if (const auto error = makeDir(parent.get(), name.c_str(), 0755, 0, 0, scratch))
    return error;
  if (const auto error = makeHome(scratch.get()))
    return error;
  return makeDir(scratch.get(), "run", 0700, kNobody, kNobody, run);
}

std::optional<SysError> resetHome(const std::string& root) {
  const UniqueFd scratch(::open(root.c_str(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
  if (!scratch)
    return failed("open");
  if (const auto error = removeEntry(scratch.get(), "home"))
    return error;
  return makeHome(scratch.get());
}

}  // namespace

bool resetSoloistHome(const Log& log, const SoloistScratch& scratch) {
  if (const auto error = resetHome(scratch.root)) {
    log.error("soloist_scratch_failed", {{"call", error->call}, {"errno", error->code}});
    return false;
  }
  return true;
}

std::optional<SoloistScratch> prepareSoloistScratch(const Log& log, const std::string& root) {
  if (const auto error = prepare(root)) {
    log.error("soloist_scratch_failed", {{"call", error->call}, {"errno", error->code}});
    return std::nullopt;
  }
  return SoloistScratch{.root = root, .home = root + "/home", .run = root + "/run"};
}

}  // namespace cadence::audiod

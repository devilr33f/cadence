#pragma once

#include "audiod/sys.h"

#include <optional>
#include <string>
#include <string_view>

// the airplay attempt's files (reference §2.2): the scratch, the avahi base, the configs, the bus socket and the fifos.
// none of them is ever reached through a symlink, and none of them is left to another owner
namespace cadence::audiod::airplay {

// path as a directory of audiod's own at mode 0700, made (with its missing parents) when absent and kept when it is one
// already; a symlink, another kind of file or a directory of another owner is refused (ELOOP, ENOTDIR, EPERM)
std::optional<SysError> privateDir(const std::string& path);
// whatever sits at path removed without following a symlink at any depth, then path made afresh at 0700; its parent
// must exist
std::optional<SysError> freshDir(const std::string& path);
// text into path at 0600, truncated first, never through a symlink
std::optional<SysError> writePrivate(const std::string& path, std::string_view text);
// a socket or a file a killed daemon left at path removed; a symlink or a directory is refused (ELOOP, EISDIR)
std::optional<SysError> removeStale(const std::string& path);
// a fifo of audiod's own at path, mode 0600: made when absent, kept when it is one already. anything else is refused
// (ELOOP for a symlink, EEXIST for a regular file or the like, EPERM for another owner's fifo): shairport would write
// into a regular file at its pipe's path
std::optional<SysError> privateFifo(const std::string& path);

}  // namespace cadence::audiod::airplay

#pragma once

#include "audiod/sys.h"

#include <optional>
#include <string>

namespace cadence::audiod {

// removes name in dir: a symlink or any other file is unlinked, a directory emptied first (made 0700 to its owner, so
// a read-only one empties too); nothing is followed at any depth. an absent name is no error
std::optional<SysError> removeEntry(int dir, const std::string& name);

}  // namespace cadence::audiod

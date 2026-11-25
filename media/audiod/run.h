#pragma once

#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"

namespace cadence::audiod {

// the daemon and its parts on loop until it stops, serving ipc; they are gone when it returns, before the children's
// lines they send. the exit code: 0, or 1 when the loop itself failed. cadence-audiod's main and the android app's
// in-process host (embedded.h) both run it
int runDaemon(Loop& loop, IpcServer& ipc, const Log& log);

}  // namespace cadence::audiod

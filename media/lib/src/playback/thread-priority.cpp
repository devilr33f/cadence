#include "playback/thread-priority.h"

#ifdef __ANDROID__
#include <sys/resource.h>
#include <unistd.h>

#include <cerrno>
#endif

namespace cadence {

ThreadPriority raiseThreadPriority(int nice) {
  ThreadPriority out;
#ifdef __ANDROID__
  out.attempted = true;
  const auto tid = static_cast<id_t>(::gettid());
  if (::setpriority(PRIO_PROCESS, tid, nice) != 0)
    out.error = errno;
  errno = 0;
  out.nice = ::getpriority(PRIO_PROCESS, tid);
#else
  static_cast<void>(nice);
#endif
  return out;
}

}  // namespace cadence

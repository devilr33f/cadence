#pragma once

#include <android/log.h>
#include <unistd.h>

#include <cstdio>
#include <string>
#include <thread>

namespace cadence::android {

// an app's stderr goes nowhere: its lines (cadence-screen's privacy-safe log, mpv's own) are copied to logcat under
// tag, a line each, by a thread reading a pipe dup'd over fd 2. once per process
inline void stderrToLogcat(const char* tag) {
  static bool done = false;
  if (done) return;
  int fds[2];
  if (::pipe(fds) != 0) return;
  ::dup2(fds[1], STDERR_FILENO);
  ::close(fds[1]);
  std::setvbuf(stderr, nullptr, _IOLBF, 0);
  done = true;
  std::thread([fd = fds[0], tag] {
    std::string line;
    char buf[1024];
    for (;;) {
      const auto n = ::read(fd, buf, sizeof buf);
      if (n <= 0) return;
      for (ssize_t i = 0; i < n; ++i) {
        if (buf[i] == '\n') {
          __android_log_write(ANDROID_LOG_INFO, tag, line.c_str());
          line.clear();
        } else if (line.size() < 4096) {
          line.push_back(buf[i]);
        }
      }
    }
  }).detach();
}

}  // namespace cadence::android

#include "audiod/daemon.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/run.h"
#include "audiod/signals.h"
#include "audiod/vendor-probe.h"

#ifdef CADENCE_MEDIA_URL
#include "url/url-source.h"
#endif

#include <chrono>
#include <cstddef>
#include <cstdio>
#include <cstdlib>
#include <span>
#include <string>
#include <string_view>

using namespace cadence::audiod;

namespace {

constexpr int kExitFailed = 1;
constexpr int kExitUsage = 2;

int usage() {
  std::fputs("usage: cadence-audiod --socket <path> [--version]\n", stderr);
  return kExitUsage;
}

}  // namespace

int main(int argc, char** argv) {
  std::string socket;
  const std::span<char*> args(argv, static_cast<std::size_t>(argc));
  // the probe child audiod forks of itself at a Configure: before any socket, loop or device, it only opens the
  // board's stock engines and exits
  if (args.size() > 1 && std::string_view(args[1]) == kProbeFlag)
    return vendorProbeMain(args, cadence::openStockLibrary);
  for (std::size_t i = 1; i < args.size(); ++i) {
    const std::string_view arg = args[i];
    if (arg == "--version") {
      std::printf("cadence-audiod %.*s\n", static_cast<int>(kVersion.size()), kVersion.data());
      return 0;
    }
    if (arg != "--socket" || i + 1 == args.size())
      return usage();
    socket = args[++i];
  }
  if (socket.empty())
    return usage();

  const auto log = Log::toStderr();
  const auto loop = Loop::create(log);
  if (!loop)
    return kExitFailed;
  // before any part could start a thread, so every thread inherits the blocked signals
  const auto signals = SignalStop::install(*loop, log);
  if (!signals)
    return kExitFailed;
  auto ipc = IpcServer::listen(*loop, log, socket);
  if (!ipc)
    return kExitFailed;
  const int code = runDaemon(*loop, *ipc, log);
  // the socket goes before anything waits
  ipc.reset();
#ifdef CADENCE_MEDIA_URL
  // a stopped decoder lets go within an interrupt poll, unless ffmpeg holds it in a name lookup: exit's cleanup must
  // not run under it
  if (!cadence::url::decodersFinished(std::chrono::milliseconds(300)))
    std::_Exit(code);
#endif
  return code;
}

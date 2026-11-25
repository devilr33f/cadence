#include "audiod/embedded.h"

#include "audiod/ipc.h"
#include "audiod/loop.h"
#include "audiod/run.h"

#include <atomic>
#include <thread>
#include <utility>

namespace cadence::audiod {

struct EmbeddedAudiod::State {
  explicit State(Log::Sink sink) : log(std::move(sink)) {}
  // the parts keep references to it: it outlives the thread
  Log log;
  std::unique_ptr<Loop> loop;
  std::unique_ptr<IpcServer> ipc;
  std::thread thread;
  std::atomic<bool> running{true};
};

std::unique_ptr<EmbeddedAudiod> EmbeddedAudiod::start(const std::string& socket, Log::Sink sink) {
  auto state = std::make_unique<State>(std::move(sink));
  state->loop = Loop::create(state->log);
  if (!state->loop)
    return nullptr;
  state->ipc = IpcServer::listen(*state->loop, state->log, socket);
  if (!state->ipc)
    return nullptr;
  State* s = state.get();
  s->thread = std::thread([s] {
    runDaemon(*s->loop, *s->ipc, s->log);
    // the socket goes as the daemon ends, so cadenced's next dial fails rather than waits
    s->ipc.reset();
    s->running.store(false);
  });
  return std::unique_ptr<EmbeddedAudiod>(new EmbeddedAudiod(std::move(state)));
}

EmbeddedAudiod::EmbeddedAudiod(std::unique_ptr<State> state) : state_(std::move(state)) {}

EmbeddedAudiod::~EmbeddedAudiod() {
  state_->loop->stop();
  if (state_->thread.joinable())
    state_->thread.join();
}

bool EmbeddedAudiod::running() const { return state_->running.load(); }

}  // namespace cadence::audiod

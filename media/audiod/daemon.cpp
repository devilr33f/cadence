#include "audiod/daemon.h"

#include <algorithm>
#include <cstddef>
#include <cstring>
#include <iterator>
#include <utility>

namespace cadence::audiod {

namespace {

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto size = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), size);
  out[size] = '\0';
}

std::string_view backendName(bool present, Backend backend) {
  if (!present)
    return "none";
  switch (backend) {
    case cadence_audiod_v1_Backend_BACKEND_ALSA:
      return "alsa";
    case cadence_audiod_v1_Backend_BACKEND_FILE:
      return "file";
    case cadence_audiod_v1_Backend_BACKEND_AAUDIO:
      return "aaudio";
    default:
      return "unknown";
  }
}

}  // namespace

std::vector<Backend> compiledBackends() {
#if defined(CADENCE_MEDIA_AAUDIO)
  return {cadence_audiod_v1_Backend_BACKEND_AAUDIO, cadence_audiod_v1_Backend_BACKEND_FILE};
#elif defined(CADENCE_MEDIA_ALSA)
  return {cadence_audiod_v1_Backend_BACKEND_ALSA, cadence_audiod_v1_Backend_BACKEND_FILE};
#else
  return {cadence_audiod_v1_Backend_BACKEND_FILE};
#endif
}

Daemon::Daemon(IpcServer& ipc, const Log& log, std::vector<Backend> backends)
    : ipc_(ipc), log_(log), backends_(std::move(backends)) {
  ipc_.on(cadence_audiod_v1_Envelope_configure_tag,
          [this](const Envelope& envelope) { configure(envelope.body.configure); });
  ipc_.on(cadence_audiod_v1_Envelope_ping_tag, [this](const Envelope& envelope) { pong(envelope.body.ping.seq); });
  ipc_.onClientGone([this] { clientGone(); });
}

// a configure this build cannot run is a cadenced bug, so it gets no Ready and the connection drops; each part
// checks the rest of its own section when it rebuilds
void Daemon::configure(const Configure& request) {
  const auto refuse = [&](std::string_view reason, std::string_view section) {
    log_.warn("configure_refused", {{"reason", reason}, {"section", section}});
    ipc_.dropClient();
  };
  if (request.has_capture && !carries(request.capture.backend))
    return refuse("backend", "capture");
  if (request.has_output && !carries(request.output.backend))
    return refuse("backend", "output");
  // 0 is stereo
  if (request.has_output && request.output.channels > 2)
    return refuse("channels", "output");
  configuration_ = request;
  log_.info("configured", {{"capture", backendName(request.has_capture, request.capture.backend)},
                           {"output", backendName(request.has_output, request.output.backend)}});
  for (const auto& part : parts_)
    part->configure(request);

  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_ready_tag;
  auto& ready = reply.body.ready;
  copyText(ready.version, kVersion);
  for (const auto backend : backends_) {
    if (ready.backends_count == std::size(ready.backends))
      break;
    ready.backends[ready.backends_count] = backend;
    ++ready.backends_count;
  }
  for (const auto& part : parts_)
    part->fillReady(ready);
  ipc_.send(reply);
  for (const auto& part : parts_)
    part->ready();
}

Daemon::~Daemon() {
  while (!parts_.empty())
    parts_.pop_back();
}

void Daemon::add(std::unique_ptr<Part> part) { parts_.push_back(std::move(part)); }

void Daemon::clientGone() {
  configuration_.reset();
  for (const auto& part : parts_)
    part->reset();
}

void Daemon::pong(std::uint64_t seq) {
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_pong_tag;
  reply.body.pong.seq = seq;
  copyText(reply.body.pong.version, kVersion);
  ipc_.send(reply);
}

bool Daemon::carries(Backend backend) const {
  return std::find(backends_.begin(), backends_.end(), backend) != backends_.end();
}

}  // namespace cadence::audiod

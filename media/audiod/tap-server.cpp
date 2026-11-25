#include "audiod/tap-server.h"

#include <vector>

#include "audiod/ring-offer.h"

#include <pb_encode.h>

#include <sys/epoll.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <chrono>
#include <cerrno>
#include <cstddef>
#include <cstring>

namespace cadence::audiod {

namespace {

// whether the peer runs as this process's uid
bool samePeerUser(int fd) {
  ucred peer{};
  socklen_t size = sizeof peer;
  return ::getsockopt(fd, SOL_SOCKET, SO_PEERCRED, &peer, &size) == 0 && size == sizeof peer && peer.uid == ::getuid();
}

// a snapshot's 16 KiB of samples and its few fields, with the envelope's tag and length
constexpr std::size_t kTapFrameBytes = cadence_audiod_v1_PcmSnapshot_size + 16;
// the send buffer asked for, so a mirror keyframe fits in one message: the kernel clamps it to twice wmem_max
constexpr int kTapSendBuffer = 8 << 20;

template <std::size_t N>
std::string textOf(const char (&field)[N]) {
  return {field, static_cast<std::size_t>(std::find(field, field + N, '\0') - field)};
}

}  // namespace

TapServer::TapServer(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback)
    : loop_(loop), log_(log), playback_(playback) {
  ipc.on(cadence_audiod_v1_Envelope_tap_config_tag,
         [this](const Envelope& envelope) { apply(envelope.body.tap_config); });
  playback_.onViz([this](const dsp::VizFrame& frame) { sendBands(frame); });
}

// the externals outlive the tap (the daemon drops its parts last first), so the listener goes before it does
TapServer::~TapServer() {
  if (externals_ != nullptr)
    externals_->onRing(nullptr);
  close();
}

// the tap outlives a new configuration: cadenced sends its config after every Ready anyway
void TapServer::configure(const Configure& /*configuration*/) {}

void TapServer::reset() { close(); }

void TapServer::useRings(ExternalSources& externals) {
  externals_ = &externals;
  externals.onRing([this](std::string_view id, const RingInput& ring) { offerRing(id, ring); });
}

void TapServer::offerRing(std::string_view id, const RingInput& ring) {
  if (!client_)
    return;
  if (const int code = sendRingOffer(client_.get(), id, ring); code != 0)
    log_.warn("tap_ring_offer_failed", {{"id", id}, {"errno", code}});
  else
    log_.info("tap_ring_offered", {{"id", id}, {"frames", static_cast<std::uint64_t>(ring.capacity())}});
}

void TapServer::apply(const cadence_audiod_v1_TapConfig& config) {
  const auto path = textOf(config.path);
  pcm_ = config.pcm;
  bands_ = config.bands;
  const bool mirrorOn = config.mirror && !mirror_;
  mirror_ = config.mirror;
  if (path != path_) {
    close();
    if (!path.empty() && listenAt(path))
      path_ = path;
  }
  playback_.setSnapshots(pcm_ && listener_.valid());
  log_.info("tap_config", {{"open", listener_.valid() ? "yes" : "no"},
                           {"pcm", pcm_ ? "on" : "off"},
                           {"bands", bands_ ? "on" : "off"},
                           {"mirror", mirror_ ? "on" : "off"}});
  arm();
  if (mirrorOn && client_)
    for (const auto& hook : clientHooks_)
      hook();
}

bool TapServer::listenAt(const std::string& path) {
  const auto fail = [&](std::string_view reason, int code) {
    log_.warn("tap_listen_failed", {{"reason", reason}, {"errno", code}});
    return false;
  };
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  if (path.size() >= sizeof address.sun_path)
    return fail("path_length", 0);
  const bool abstract = path.front() == '@';
  std::memcpy(address.sun_path, path.data(), path.size());
  socklen_t length = sizeof address;
  if (abstract) {
    // an abstract name is the bytes after a nul, exactly as long as the name
    address.sun_path[0] = '\0';
    length = static_cast<socklen_t>(offsetof(sockaddr_un, sun_path) + path.size());
  } else {
    struct stat existing {};
    if (::lstat(path.c_str(), &existing) == 0) {
      if (!S_ISSOCK(existing.st_mode))
        return fail("not_socket", 0);
      ::unlink(path.c_str());
    }
  }
  UniqueFd listener(::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_NONBLOCK | SOCK_CLOEXEC, 0));
  if (!listener)
    return fail("socket", errno);
  if (!abstract && ::fchmod(listener.get(), 0600) < 0)
    return fail("fchmod", errno);
  if (::bind(listener.get(), reinterpret_cast<const sockaddr*>(&address), length) < 0)
    return fail("bind", errno);
  if (::listen(listener.get(), 1) < 0)
    return fail("listen", errno);
  const auto watched = loop_.watch(listener.get(), EPOLLIN, [this](std::uint32_t) { accept(); });
  if (!watched.ok())
    return fail("epoll_ctl", watched.error().code);
  listener_ = std::move(listener);
  listenerWatch_ = watched.value();
  return true;
}

void TapServer::close() {
  dropClient();
  if (listenerWatch_)
    loop_.unwatch(*listenerWatch_);
  listenerWatch_.reset();
  listener_.reset();
  if (!path_.empty() && path_.front() != '@')
    ::unlink(path_.c_str());
  path_.clear();
  if (timer_)
    loop_.cancel(*timer_);
  timer_.reset();
  playback_.setSnapshots(false);
}

void TapServer::accept() {
  for (;;) {
    UniqueFd accepted(::accept4(listener_.get(), nullptr, nullptr, SOCK_NONBLOCK | SOCK_CLOEXEC));
    if (!accepted) {
      if (errno == EINTR || errno == ECONNABORTED)
        continue;
      return;
    }
    if (path_.front() == '@' && !samePeerUser(accepted.get())) {
      // an abstract name has no mode, unlike a path made 0600: only this uid's processes (on android, the app's
      // screen process beside the daemon's) may read the mix or take a ring
      log_.warn("tap_client_refused", {{"reason", "uid"}});
      continue;
    }
    // the newest screen wins: one that restarted reconnects before the old socket reads as gone
    dropClient();
    const auto watched = loop_.watch(accepted.get(), EPOLLIN | EPOLLRDHUP, [this](std::uint32_t) { drain(); });
    if (!watched.ok())
      continue;
    // a mirror keyframe is one message: as large a buffer as the kernel gives
    const int buffer = kTapSendBuffer;
    ::setsockopt(accepted.get(), SOL_SOCKET, SO_SNDBUF, &buffer, sizeof buffer);
    client_ = std::move(accepted);
    clientWatch_ = watched.value();
    sentSeq_ = 0;
    sentGroupClock_.reset();
    log_.info("tap_client_connected");
    if (externals_ != nullptr)
      externals_->eachRing([this](std::string_view id, const RingInput& ring) { offerRing(id, ring); });
    arm();
    for (const auto& hook : clientHooks_)
      hook();
  }
}

void TapServer::drain() {
  std::array<std::uint8_t, 256> scratch{};
  for (;;) {
    const auto size = ::recv(client_.get(), scratch.data(), scratch.size(), MSG_DONTWAIT);
    if (size > 0)
      continue;
    if (size < 0 && (errno == EINTR))
      continue;
    if (size < 0 && (errno == EAGAIN || errno == EWOULDBLOCK))
      return;
    log_.info("tap_client_gone");
    dropClient();
    return;
  }
}

void TapServer::dropClient() {
  if (clientWatch_)
    loop_.unwatch(*clientWatch_);
  clientWatch_.reset();
  client_.reset();
  stalled_ = false;
  mirrorStalled_ = false;
}

void TapServer::arm() {
  if (timer_ || !client_ || !(pcm_ || mirror_ || groupClock_))
    return;
  timer_ = loop_.after(kTapPcmEvery, [this] {
    timer_.reset();
    tick();
    arm();
  });
}

void TapServer::tick() {
  if (pcm_ && playback_.snapshot(snapshot_) && snapshot_.seq != sentSeq_) {
    sentSeq_ = snapshot_.seq;
    cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
    envelope.which_body = cadence_audiod_v1_TapEnvelope_pcm_tag;
    auto& pcm = envelope.body.pcm;
    pcm.seq = snapshot_.seq;
    pcm.sample_rate = 48000;
    pcm.channels = 2;
    pcm.mono_ns = snapshot_.monoNs;
    const auto bytes = snapshot_.frames * 2 * sizeof(float);
    static_assert(sizeof pcm.samples.bytes >= PcmSnapshot::kFrames * 2 * sizeof(float));
    // little-endian f32 on every board's cpu, as the wire says
    std::memcpy(pcm.samples.bytes, snapshot_.samples.data(), bytes);
    pcm.samples.size = static_cast<pb_size_t>(bytes);
    send(envelope);
  }
  const auto now = Loop::Clock::now();
  if (mirror_ && now >= nextClock_) {
    nextClock_ = now + kTapClockEvery;
    auto* clock = playback_.clock();
    if (auto sample = clock != nullptr ? clock->sample() : std::nullopt) {
      // a tv that leads a group: its mirror's sound is heard when the group hears it, and so are the pictures
      if (auto* group = externals_ != nullptr ? externals_->groupFeed() : nullptr)
        sample = heardByGroup(*sample, group->leadNs());
      cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
      envelope.which_body = cadence_audiod_v1_TapEnvelope_clock_tag;
      envelope.body.clock.media_ns = sample->mediaNs;
      envelope.body.clock.mono_ns = sample->monoNs;
      envelope.body.clock.latency_ns = sample->latencyNs;
      send(envelope);
    }
  }
  sendGroupClock(now);
}

void TapServer::sendGroupClock(Loop::Clock::time_point now) {
  if (!groupClock_)
    return;
  const auto monoNs = std::chrono::duration_cast<std::chrono::nanoseconds>(now.time_since_epoch()).count();
  const auto sample = groupClock_(static_cast<std::int64_t>(monoNs));
  if (!sample) {
    sentGroupClock_.reset();
    return;
  }
  // a new epoch (a seek, a new track, a resume) or a stop goes out at once: a follower seeks or pauses on it
  const bool changed =
      !sentGroupClock_ || sentGroupClock_->epoch != sample->epoch || sentGroupClock_->playing != sample->playing;
  if (!changed && now < nextGroupClock_)
    return;
  nextGroupClock_ = now + kTapClockEvery;
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  envelope.which_body = cadence_audiod_v1_TapEnvelope_group_clock_tag;
  envelope.body.group_clock.media_ns = sample->mediaNs;
  envelope.body.group_clock.mono_ns = sample->monoNs;
  envelope.body.group_clock.epoch = sample->epoch;
  envelope.body.group_clock.playing = sample->playing;
  if (send(envelope))
    sentGroupClock_ = sample;
}

void TapServer::sendBands(const dsp::VizFrame& frame) {
  if (!bands_ || !client_)
    return;
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  envelope.which_body = cadence_audiod_v1_TapEnvelope_bands_tag;
  static_assert(sizeof envelope.body.bands.bands.bytes == dsp::kVizBands);
  std::copy(frame.begin(), frame.end(), envelope.body.bands.bands.bytes);
  envelope.body.bands.bands.size = static_cast<pb_size_t>(frame.size());
  send(envelope);
}

TapServer::Sent TapServer::sendMirror(std::span<const std::uint8_t> encoded) {
  if (!client_ || !mirror_)
    return Sent::NoClient;
  if (::send(client_.get(), encoded.data(), encoded.size(), MSG_NOSIGNAL | MSG_DONTWAIT) >= 0) {
    mirrorStalled_ = false;
    return Sent::Sent;
  }
  if (const int code = errno; code == EAGAIN || code == EWOULDBLOCK || code == EMSGSIZE || code == ENOBUFS) {
    if (!mirrorStalled_)
      log_.warn("tap_mirror_dropped", {{"errno", code}, {"bytes", encoded.size()}});
    mirrorStalled_ = true;
    return Sent::Dropped;
  }
  dropClient();
  return Sent::NoClient;
}

bool TapServer::send(const cadence_audiod_v1_TapEnvelope& envelope) {
  if (!client_)
    return false;
  // on the heap: 16 KiB a call is too much for the loop thread's stack frame to carry each tick
  thread_local std::vector<std::uint8_t> frame(kTapFrameBytes);
  pb_ostream_t stream = pb_ostream_from_buffer(frame.data(), frame.size());
  if (!pb_encode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &envelope)) {
    log_.error("tap_encode_failed", {{"type", envelope.which_body}});
    return false;
  }
  if (::send(client_.get(), frame.data(), stream.bytes_written, MSG_NOSIGNAL | MSG_DONTWAIT) >= 0) {
    stalled_ = false;
    return true;
  }
  if (errno == EAGAIN || errno == EWOULDBLOCK) {
    if (!stalled_)
      log_.warn("tap_send_dropped", {{"type", envelope.which_body}});
    stalled_ = true;
    return false;
  }
  dropClient();
  return false;
}

}  // namespace cadence::audiod

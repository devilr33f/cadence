#include "player/tap-client.h"

#include "cadence/audiod/v1/audiod.pb.h"
#include "ipc/backoff.h"
#include "ipc/log.h"
#include "ipc/seqpacket.h"

#include <pb_decode.h>

#include <poll.h>
#include <sys/socket.h>
#include <unistd.h>

#include <cerrno>
#include <chrono>
#include <cstring>
#include <optional>

namespace cadence::screen::player {

namespace {

// every tap body fits, a mirror frame's fragment too (audiod keeps each under 256 KiB; a larger unit comes as
// fragments, MirrorJoin); a larger message is read away whole and dropped
constexpr std::size_t kFrameBytes = 512 * 1024;
constexpr std::size_t kMaxFds = 4;

void closeAll(std::span<const int> fds) {
  for (const int fd : fds)
    if (fd >= 0) ::close(fd);
}

std::int64_t monoMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

bool collectData(pb_istream_t* stream, const pb_field_t*, void** arg) {
  auto* out = static_cast<std::vector<std::uint8_t>*>(*arg);
  out->resize(stream->bytes_left);
  return pb_read(stream, out->data(), out->size());
}

// nanopb clears a oneof's member when the oneof is set, its callbacks with it: a mirror frame (field 4) decodes as a
// MirrorFrame of its own with its data callback set (media/tests/support/mirror-frames.h does the same)
std::optional<bool> mirrorFrame(std::span<const std::uint8_t> bytes, const TapSinks& sinks, MirrorJoin* join) {
  pb_istream_t stream = pb_istream_from_buffer(bytes.data(), bytes.size());
  pb_wire_type_t wire{};
  std::uint32_t tag = 0;
  bool eof = false;
  if (!pb_decode_tag(&stream, &wire, &tag, &eof) || tag != cadence_audiod_v1_TapEnvelope_mirror_frame_tag ||
      wire != PB_WT_STRING)
    return std::nullopt;
  pb_istream_t sub{};
  if (!pb_make_string_substream(&stream, &sub)) return false;
  std::vector<std::uint8_t> data;
  cadence_audiod_v1_MirrorFrame f = cadence_audiod_v1_MirrorFrame_init_zero;
  f.data.funcs.decode = collectData;
  f.data.arg = &data;
  const bool ok = pb_decode(&sub, &cadence_audiod_v1_MirrorFrame_msg, &f);
  pb_close_string_substream(&stream, &sub);
  if (!ok) return false;
  if (f.total_size != 0) {
    // a fragment: the unit reaches the sink once whole
    if (join == nullptr) return false;
    auto unit = join->add(f.seq, f.offset, f.total_size, data);
    if (unit && sinks.mirror) sinks.mirror(TapSinks::MirrorFrame{f.session_id, f.pts_ns, f.config, f.keyframe, *unit});
    return true;
  }
  if (sinks.mirror) sinks.mirror(TapSinks::MirrorFrame{f.session_id, f.pts_ns, f.config, f.keyframe, data});
  return true;
}

}  // namespace

void MirrorJoin::drop() {
  if (active_) dropped_++;
  active_ = false;
  unit_.clear();
}

std::optional<std::vector<std::uint8_t>> MirrorJoin::add(std::uint32_t seq, std::uint32_t offset, std::uint32_t total,
                                                         std::span<const std::uint8_t> data) {
  if (offset == 0) {
    // a new unit: one left unfinished is lost
    drop();
    if (total > kMaxUnit || data.size() > total) {
      dropped_++;
      return std::nullopt;
    }
    active_ = true;
    seq_ = seq;
    total_ = total;
    unit_.reserve(total);
  } else if (!active_ || seq != seq_ || total != total_ || offset != unit_.size() ||
             data.size() > total_ - unit_.size()) {
    drop();
    return std::nullopt;
  }
  unit_.insert(unit_.end(), data.begin(), data.end());
  if (unit_.size() < total_) return std::nullopt;
  active_ = false;
  return std::move(unit_);
}

TapFrame dispatchTapFrame(std::span<const std::uint8_t> frame, std::span<const int> fds, const TapSinks& sinks,
                          std::optional<RingOffer>& ring, std::string* error, MirrorJoin* join) {
  if (const auto m = mirrorFrame(frame, sinks, join)) {
    closeAll(fds);
    if (!*m && error != nullptr) *error = "decode";
    return *m ? TapFrame::Mirror : TapFrame::Refused;
  }
  // the ring offer's own decoder takes the fds (and refuses a frame with the wrong count)
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  pb_istream_t stream = pb_istream_from_buffer(frame.data(), frame.size());
  if (!pb_decode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &envelope)) {
    closeAll(fds);
    if (error != nullptr) *error = "decode";
    return TapFrame::Refused;
  }
  if (envelope.which_body == cadence_audiod_v1_TapEnvelope_ring_offer_tag) {
    ring = ringOfferFrom(frame, fds, error);
    return ring ? TapFrame::Ring : TapFrame::Refused;
  }
  // nothing else carries fds: a stray one is closed, never leaked
  closeAll(fds);
  switch (envelope.which_body) {
    case cadence_audiod_v1_TapEnvelope_pcm_tag: {
      const auto& p = envelope.body.pcm;
      if (p.channels == 0 || p.channels > 2 || p.sample_rate == 0 || p.samples.size % (4 * p.channels) != 0) {
        if (error != nullptr) *error = "pcm";
        return TapFrame::Refused;
      }
      // little-endian f32 on every target we build for
      std::array<float, 4096> samples{};
      const std::size_t count = p.samples.size / 4;
      std::memcpy(samples.data(), p.samples.bytes, count * 4);
      if (sinks.pcm) sinks.pcm(std::span<const float>(samples.data(), count), p.channels, p.sample_rate, p.mono_ns);
      return TapFrame::Pcm;
    }
    case cadence_audiod_v1_TapEnvelope_bands_tag: {
      std::array<std::uint8_t, 16> bands{};
      const auto& b = envelope.body.bands.bands;
      std::memcpy(bands.data(), b.bytes, std::min<std::size_t>(b.size, bands.size()));
      if (sinks.bands) sinks.bands(bands);
      return TapFrame::Bands;
    }
    case cadence_audiod_v1_TapEnvelope_clock_tag: {
      // the presentation clock: airplay mirroring's (stream j), for the mirror presenter
      const auto& c = envelope.body.clock;
      if (sinks.clock) sinks.clock(c.media_ns, c.mono_ns, c.latency_ns);
      return TapFrame::Clock;
    }
    case cadence_audiod_v1_TapEnvelope_group_clock_tag: {
      const auto& g = envelope.body.group_clock;
      if (sinks.groupClock) sinks.groupClock(FollowClock{g.media_ns, g.mono_ns, g.epoch, g.playing});
      return TapFrame::GroupClock;
    }
    default:
      if (error != nullptr) *error = "body";
      return TapFrame::Refused;
  }
}

TapClient::TapClient(TapSinks sinks) : sinks_(std::move(sinks)) {}

TapClient::~TapClient() { stop(); }

void TapClient::connect(const std::string& path) {
  if (path == path_ && thread_.joinable()) return;
  stop();
  path_ = path;
  if (path.empty()) return;
  stopping_ = false;
  thread_ = std::thread([this, path] { run(path, -1); });
}

void TapClient::adopt(int fd) {
  stop();
  path_.clear();
  stopping_ = false;
  thread_ = std::thread([this, fd] { run({}, fd); });
}

void TapClient::stop() {
  stopping_ = true;
  // wakes a blocked poll: the read side sees the shutdown as a hang-up
  if (const int fd = fd_.load(); fd >= 0) ::shutdown(fd, SHUT_RDWR);
  if (thread_.joinable()) thread_.join();
  path_.clear();
}

std::vector<RingOffer> TapClient::takeRings() {
  std::lock_guard lock(mu_);
  std::vector<RingOffer> out;
  out.swap(rings_);
  return out;
}

TapClient::Counts TapClient::counts() const {
  std::lock_guard lock(mu_);
  return counts_;
}

void TapClient::run(std::string path, int adopted) {
  ipc::Backoff backoff(100, 5000, 10000);
  while (!stopping_) {
    int fd = adopted;
    adopted = -1;
    if (fd < 0) {
      if (path.empty()) return;
      std::string error;
      auto socket = ipc::SeqpacketSocket::connect(path, &error);
      if (!socket) {
        const auto waitMs = backoff.next();
        for (std::int64_t t = 0; t < waitMs && !stopping_; t += 50)
          std::this_thread::sleep_for(std::chrono::milliseconds(50));
        continue;
      }
      fd = ::dup(socket->fd());
      socket->close();
      if (fd < 0) continue;
    }
    {
      std::lock_guard lock(mu_);
      counts_.connects++;
    }
    CADENCE_LOG(Info, "tap_connected");
    fd_ = fd;
    const auto since = monoMs();
    const bool more = serve(fd);
    fd_ = -1;
    ::close(fd);
    CADENCE_LOG(Info, "tap_closed");
    backoff.connectedFor(monoMs() - since);
    if (!more || path.empty()) return;
  }
}

bool TapClient::serve(int fd) {
  std::vector<std::uint8_t> frame(kFrameBytes);
  alignas(cmsghdr) std::array<char, CMSG_SPACE(sizeof(int) * kMaxFds)> control{};
  while (!stopping_) {
    pollfd p{fd, POLLIN, 0};
    const int ready = ::poll(&p, 1, 200);
    if (ready < 0 && errno == EINTR) continue;
    if (ready < 0) return true;
    if (ready == 0) continue;
    iovec data{frame.data(), frame.size()};
    msghdr message{};
    message.msg_iov = &data;
    message.msg_iovlen = 1;
    message.msg_control = control.data();
    message.msg_controllen = control.size();
    const ssize_t size = ::recvmsg(fd, &message, MSG_CMSG_CLOEXEC | MSG_DONTWAIT);
    if (size < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK)) continue;
    if (size <= 0) return !stopping_;
    std::vector<int> fds;
    for (auto* h = CMSG_FIRSTHDR(&message); h != nullptr; h = CMSG_NXTHDR(&message, h)) {
      if (h->cmsg_level != SOL_SOCKET || h->cmsg_type != SCM_RIGHTS) continue;
      const auto n = (h->cmsg_len - CMSG_LEN(0)) / sizeof(int);
      const auto first = fds.size();
      fds.resize(first + n);
      std::memcpy(fds.data() + first, CMSG_DATA(h), n * sizeof(int));
    }
    std::optional<RingOffer> ring;
    std::string error;
    TapFrame kind = TapFrame::Refused;
    if ((message.msg_flags & (MSG_TRUNC | MSG_CTRUNC)) != 0) {
      closeAll(fds);
      error = "truncated";
    } else {
      kind = dispatchTapFrame(std::span(frame.data(), static_cast<std::size_t>(size)), fds, sinks_, ring, &error, &join_);
    }
    bool wake = false;
    {
      std::lock_guard lock(mu_);
      counts_.frames++;
      switch (kind) {
        case TapFrame::Pcm:
          counts_.pcm++;
          break;
        case TapFrame::Bands:
          counts_.bands++;
          break;
        case TapFrame::Ring:
          counts_.rings++;
          // a newer offer for the same session replaces the queued one (its fds close with it)
          std::erase_if(rings_, [&](const RingOffer& r) { return r.sessionId == ring->sessionId; });
          rings_.push_back(std::move(*ring));
          wake = true;
          break;
        case TapFrame::Refused:
          counts_.refused++;
          break;
        case TapFrame::Mirror:
          counts_.mirror++;
          break;
        default:
          break;
      }
    }
    if (kind == TapFrame::Refused) CADENCE_LOG(Warn, "tap_frame_refused", {{"code", ipc::Token(error)}});
    if (kind == TapFrame::Ring) CADENCE_LOG(Info, "tap_ring_offered");
    if (wake && sinks_.ringReady) sinks_.ringReady();
  }
  return false;
}

}  // namespace cadence::screen::player

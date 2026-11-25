#include "audiod/airplay-uxplay-mirror.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <pb_encode.h>

#include <algorithm>
#include <array>
#include <cstring>
#include <utility>

namespace cadence::audiod::uxplay {

namespace {

constexpr std::array<std::uint8_t, 4> kStartCode{0, 0, 0, 1};

bool isConfig(std::uint8_t header, bool h265) {
  if (h265) {
    const auto type = (header >> 1) & 0x3f;
    // vps, sps, pps
    return type == 32 || type == 33 || type == 34;
  }
  const auto type = header & 0x1f;
  return type == 7 || type == 8;
}

bool isKeyframe(std::uint8_t header, bool h265) {
  if (h265) {
    const auto type = (header >> 1) & 0x3f;
    // the irap pictures: bla, idr, cra
    return type >= 16 && type <= 21;
  }
  return (header & 0x1f) == 5;
}

bool startsAt(std::span<const std::uint8_t> data, std::size_t at) {
  return at + kStartCode.size() <= data.size() && std::equal(kStartCode.begin(), kStartCode.end(), data.begin() + static_cast<std::ptrdiff_t>(at));
}

// the next 4-byte start code at or after from, else the end; libairplay writes only 4-byte ones
std::size_t nextStart(std::span<const std::uint8_t> data, std::size_t from) {
  for (std::size_t i = from; i + kStartCode.size() <= data.size(); ++i)
    if (data[i] == 0 && data[i + 1] == 0 && data[i + 2] == 0 && data[i + 3] == 1)
      return i;
  return data.size();
}

struct Payload {
  std::span<const std::uint8_t> data;
};

bool writeBytes(pb_ostream_t* stream, const pb_field_t* field, void* const* arg) {
  const auto* payload = static_cast<const Payload*>(*arg);
  return pb_encode_tag_for_field(stream, field) && pb_encode_string(stream, payload->data.data(), payload->data.size());
}

}  // namespace

AccessUnit splitAccessUnit(std::span<const std::uint8_t> annexB, bool h265) {
  AccessUnit out;
  if (!startsAt(annexB, 0))
    return out;
  std::size_t at = 0;
  while (at < annexB.size()) {
    const auto body = at + kStartCode.size();
    const auto end = nextStart(annexB, body);
    if (body >= end) {
      at = end;
      continue;
    }
    const auto nal = annexB.subspan(at, end - at);
    const auto header = annexB[body];
    // the forbidden zero bit
    if ((header & 0x80) != 0)
      return AccessUnit{};
    auto& into = isConfig(header, h265) ? out.config : out.frame;
    into.insert(into.end(), nal.begin(), nal.end());
    out.keyframe = out.keyframe || isKeyframe(header, h265);
    at = end;
  }
  out.valid = !out.frame.empty() || !out.config.empty();
  return out;
}

std::vector<std::uint8_t> encodeMirrorFrame(std::string_view sessionId, std::int64_t ptsNs, bool config, bool keyframe,
                                            std::span<const std::uint8_t> data) {
  return encodeMirrorFrame(sessionId, ptsNs, config, keyframe, data, MirrorFragment{});
}

std::vector<std::uint8_t> encodeMirrorFrame(std::string_view sessionId, std::int64_t ptsNs, bool config, bool keyframe,
                                            std::span<const std::uint8_t> data, MirrorFragment fragment) {
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  envelope.which_body = cadence_audiod_v1_TapEnvelope_mirror_frame_tag;
  auto& frame = envelope.body.mirror_frame;
  const auto idBytes = std::min(sessionId.size(), sizeof frame.session_id - 1);
  std::memcpy(frame.session_id, sessionId.data(), idBytes);
  frame.session_id[idBytes] = '\0';
  frame.pts_ns = ptsNs;
  frame.config = config;
  frame.keyframe = keyframe;
  frame.seq = fragment.seq;
  frame.offset = fragment.offset;
  frame.total_size = fragment.total;
  Payload payload{data};
  frame.data.funcs.encode = writeBytes;
  frame.data.arg = &payload;
  std::size_t size = 0;
  if (!pb_get_encoded_size(&size, &cadence_audiod_v1_TapEnvelope_msg, &envelope))
    return {};
  std::vector<std::uint8_t> out(size);
  pb_ostream_t stream = pb_ostream_from_buffer(out.data(), out.size());
  if (!pb_encode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &envelope))
    return {};
  out.resize(stream.bytes_written);
  return out;
}

void MirrorForwarder::start(std::string sessionId) {
  sessionId_ = std::move(sessionId);
  config_.clear();
  configPending_ = false;
  waitKeyframe_ = true;
}

void MirrorForwarder::stop() {
  sessionId_.clear();
  config_.clear();
  configPending_ = false;
  waitKeyframe_ = true;
}

void MirrorForwarder::clientConnected() {
  if (!active())
    return;
  configPending_ = !config_.empty();
  waitKeyframe_ = true;
}

bool MirrorForwarder::sendConfig(std::int64_t ptsNs) {
  const auto encoded = encodeMirrorFrame(sessionId_, ptsNs, true, false, config_);
  if (encoded.empty() || send_(encoded) != SendResult::Sent)
    return false;
  configPending_ = false;
  return true;
}

void MirrorForwarder::unit(const AccessUnit& unit, std::int64_t ptsNs) {
  if (!active())
    return;
  if (!unit.valid) {
    // what follows may reference what was lost
    waitKeyframe_ = true;
    ++dropped_;
    return;
  }
  if (!unit.config.empty() && unit.config != config_) {
    config_ = unit.config;
    configPending_ = true;
  }
  if (configPending_ && !sendConfig(ptsNs)) {
    waitKeyframe_ = true;
    if (!unit.frame.empty())
      ++dropped_;
    return;
  }
  if (unit.frame.empty())
    return;
  if (waitKeyframe_ && !unit.keyframe) {
    ++dropped_;
    return;
  }
  if (!sendFrame(unit, ptsNs)) {
    waitKeyframe_ = true;
    ++dropped_;
    return;
  }
  waitKeyframe_ = false;
  ++sent_;
}

bool MirrorForwarder::sendFrame(const AccessUnit& unit, std::int64_t ptsNs) {
  ++seq_;
  const std::span<const std::uint8_t> frame(unit.frame);
  if (frame.size() <= kMirrorFragmentBytes) {
    const auto encoded = encodeMirrorFrame(sessionId_, ptsNs, false, unit.keyframe, frame);
    return !encoded.empty() && send_(encoded) == SendResult::Sent;
  }
  const auto total = static_cast<std::uint32_t>(frame.size());
  for (std::size_t at = 0; at < frame.size(); at += kMirrorFragmentBytes) {
    const auto part = frame.subspan(at, std::min(kMirrorFragmentBytes, frame.size() - at));
    const auto encoded = encodeMirrorFrame(sessionId_, ptsNs, false, unit.keyframe, part,
                                           MirrorFragment{seq_, static_cast<std::uint32_t>(at), total});
    // the screen drops a unit it got part of, so nothing more of this one goes
    if (encoded.empty() || send_(encoded) != SendResult::Sent)
      return false;
  }
  return true;
}

}  // namespace cadence::audiod::uxplay

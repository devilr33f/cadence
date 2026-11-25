#pragma once

#include "cadence/audiod/v1/audiod.pb.h"

#include <pb_decode.h>

#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence::test_support {

// a TapEnvelope as the screen reads it, its mirror frame's data (a callback field) collected
struct TapMessage {
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  std::vector<std::uint8_t> data;

  [[nodiscard]] bool mirror() const { return envelope.which_body == cadence_audiod_v1_TapEnvelope_mirror_frame_tag; }
  [[nodiscard]] const cadence_audiod_v1_MirrorFrame& frame() const { return envelope.body.mirror_frame; }
};

inline bool collectBytes(pb_istream_t* stream, const pb_field_t* /*field*/, void** arg) {
  auto* out = static_cast<std::vector<std::uint8_t>*>(*arg);
  out->resize(stream->bytes_left);
  return pb_read(stream, out->data(), out->size());
}

// nanopb clears a oneof member when the oneof is set, its callbacks with it: a mirror frame (field 4) is decoded as a
// MirrorFrame of its own, with the data callback set, and every other body as the envelope. a screen's reader does the
// same (or the contract gives the oneof nanopb's submsg_callback)
inline std::optional<TapMessage> decodeTap(std::span<const std::uint8_t> bytes) {
  TapMessage message;
  pb_istream_t stream = pb_istream_from_buffer(bytes.data(), bytes.size());
  pb_wire_type_t wire{};
  std::uint32_t tag = 0;
  bool eof = false;
  if (pb_decode_tag(&stream, &wire, &tag, &eof) && tag == cadence_audiod_v1_TapEnvelope_mirror_frame_tag &&
      wire == PB_WT_STRING) {
    pb_istream_t frame{};
    if (!pb_make_string_substream(&stream, &frame))
      return std::nullopt;
    auto& out = message.envelope.body.mirror_frame;
    out = cadence_audiod_v1_MirrorFrame_init_zero;
    out.data.funcs.decode = collectBytes;
    out.data.arg = &message.data;
    const bool ok = pb_decode(&frame, &cadence_audiod_v1_MirrorFrame_msg, &out);
    pb_close_string_substream(&stream, &frame);
    out.data.arg = nullptr;
    if (!ok)
      return std::nullopt;
    message.envelope.which_body = cadence_audiod_v1_TapEnvelope_mirror_frame_tag;
    return message;
  }
  stream = pb_istream_from_buffer(bytes.data(), bytes.size());
  if (!pb_decode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &message.envelope))
    return std::nullopt;
  return message;
}

}  // namespace cadence::test_support

#include "url/ffmpeg.h"

#include <catch2/catch_test_macros.hpp>

#include <set>
#include <string>
#include <string_view>

namespace {

using Names = std::set<std::string>;

Names decoders() {
  Names names;
  void* opaque = nullptr;
  while (const AVCodec* codec = av_codec_iterate(&opaque))
    if (av_codec_is_decoder(codec) != 0)
      names.insert(codec->name);
  return names;
}

Names encoders() {
  Names names;
  void* opaque = nullptr;
  while (const AVCodec* codec = av_codec_iterate(&opaque))
    if (av_codec_is_encoder(codec) != 0)
      names.insert(codec->name);
  return names;
}

Names demuxers() {
  Names names;
  void* opaque = nullptr;
  while (const AVInputFormat* format = av_demuxer_iterate(&opaque))
    names.insert(format->name);
  return names;
}

Names muxers() {
  Names names;
  void* opaque = nullptr;
  while (const AVOutputFormat* format = av_muxer_iterate(&opaque))
    names.insert(format->name);
  return names;
}

Names parsers() {
  Names names;
  void* opaque = nullptr;
  while (const AVCodecParser* parser = av_parser_iterate(&opaque))
    for (const int id : parser->codec_ids)
      if (id != AV_CODEC_ID_NONE)
        names.insert(avcodec_get_name(static_cast<AVCodecID>(id)));
  return names;
}

Names inputProtocols() {
  Names names;
  void* opaque = nullptr;
  while (const char* name = avio_enum_protocols(&opaque, 0))
    names.insert(name);
  return names;
}

}  // namespace

// M4b-D15: shared libraries under the lgpl, version 3 or later, never gpl or nonfree
TEST_CASE("FfmpegIsLgplVersion3OrLater") {
  constexpr std::string_view lgpl3 = "LGPL version 3 or later";
  CHECK(std::string_view(avutil_license()) == lgpl3);
  CHECK(std::string_view(avcodec_license()) == lgpl3);
  CHECK(std::string_view(avformat_license()) == lgpl3);
}

// M4b-D9's components and what they pull in (the hls demuxer brings ac3 and eac3 along), nothing more
TEST_CASE("FfmpegCarriesOnlyM4bComponents") {
  CHECK(decoders() == Names{"aac", "flac", "mp3float", "opus", "pcm_f32le", "pcm_s16le", "pcm_s24le", "vorbis"});
  CHECK(encoders().empty());
  CHECK(demuxers() ==
        Names{"aac", "ac3", "eac3", "flac", "hls", "mov,mp4,m4a,3gp,3g2,mj2", "mp3", "mpegts", "ogg", "wav"});
  CHECK(muxers().empty());
  CHECK(parsers() == Names{"aac", "ac3", "eac3", "flac", "mp1", "mp2", "mp3", "mp3adu", "opus", "vorbis"});
  CHECK(inputProtocols() == Names{"crypto", "http", "https", "tcp", "tls"});
}

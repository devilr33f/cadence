#include "url/url-input.h"

#include "url/ffmpeg.h"
#include "url/pcm-convert.h"

#include <cerrno>
#include <utility>

namespace cadence::url {

namespace {

std::string tag(const AVDictionary* metadata, const char* key) {
  const auto* entry = av_dict_get(metadata, key, nullptr, 0);
  return entry == nullptr ? std::string() : std::string(entry->value);
}

// the container's tag, else the stream's (ogg keeps its comments on the stream)
std::string tag(const AVFormatContext* format, const AVStream* stream, const char* key) {
  auto value = tag(format->metadata, key);
  return value.empty() ? tag(stream->metadata, key) : value;
}

}  // namespace

std::string_view urlCode(int error, bool opening) {
  switch (error) {
    case AVERROR_HTTP_NOT_FOUND:
      return kUrlNotFound;
    case AVERROR_HTTP_BAD_REQUEST:
    case AVERROR_HTTP_UNAUTHORIZED:
    case AVERROR_HTTP_FORBIDDEN:
    case AVERROR_HTTP_TOO_MANY_REQUESTS:
    case AVERROR_HTTP_OTHER_4XX:
    case AVERROR_HTTP_SERVER_ERROR:
      return kUrlHttpError;
    case AVERROR(ETIMEDOUT):
      return kUrlTimeout;
    case AVERROR_EXIT:
      return kUrlInterrupted;
    case AVERROR_STREAM_NOT_FOUND:
    case AVERROR_DECODER_NOT_FOUND:
    case AVERROR_DEMUXER_NOT_FOUND:
    case AVERROR_PROTOCOL_NOT_FOUND:
      return opening ? kUrlUnsupported : kUrlReadFailed;
    default:
      return opening ? kUrlOpenFailed : kUrlReadFailed;
  }
}

UrlInput::UrlInput(std::vector<FfmpegOption> options, Interrupt interrupt)
    : options_(std::move(options)), interrupt_(std::move(interrupt)) {}

UrlInput::~UrlInput() {
  av_frame_free(&frame_);
  av_packet_free(&packet_);
  avcodec_free_context(&codec_);
  avformat_close_input(&format_);
}

Result<std::unique_ptr<UrlInput>, std::string_view> UrlInput::open(const std::string& url,
                                                                 std::vector<FfmpegOption> options,
                                                                 Interrupt interrupt) {
  std::unique_ptr<UrlInput> input(new UrlInput(std::move(options), std::move(interrupt)));
  input->format_ = avformat_alloc_context();
  input->packet_ = av_packet_alloc();
  input->frame_ = av_frame_alloc();
  if (input->format_ == nullptr || input->packet_ == nullptr || input->frame_ == nullptr)
    return kUrlOpenFailed;
  input->format_->interrupt_callback = {&UrlInput::interrupted, input.get()};
  // every open behind this one (a playlist's segments and keys) gets the same options; ffmpeg would pass on only some
  input->format_->opaque = input.get();
  input->defaultOpen_ = input->format_->io_open;
  input->format_->io_open = &UrlInput::openNested;

  FfmpegDictionary dictionary(input->options_);
  // on failure it frees the context and nulls the pointer
  if (const int opened = avformat_open_input(&input->format_, url.c_str(), nullptr, dictionary.get()); opened < 0)
    return urlCode(opened, true);
  if (const auto failed = input->openStream())
    return *failed;
  return input;
}

std::optional<std::string_view> UrlInput::openStream() {
  if (const int found = avformat_find_stream_info(format_, nullptr); found < 0)
    return urlCode(found, true);
  const AVCodec* decoder = nullptr;
  stream_ = av_find_best_stream(format_, AVMEDIA_TYPE_AUDIO, -1, -1, &decoder, 0);
  if (stream_ < 0)
    return urlCode(stream_, true);
  const auto* stream = format_->streams[stream_];
  if (stream->codecpar->ch_layout.nb_channels > 2)
    return kUrlUnsupported;
  codec_ = avcodec_alloc_context3(decoder);
  if (codec_ == nullptr || avcodec_parameters_to_context(codec_, stream->codecpar) < 0)
    return kUrlOpenFailed;
  if (const int opened = avcodec_open2(codec_, decoder, nullptr); opened < 0)
    return urlCode(opened, true);
  // everything else in the container is never read into a packet
  for (unsigned index = 0; index < format_->nb_streams; ++index)
    if (static_cast<int>(index) != stream_)
      format_->streams[index]->discard = AVDISCARD_ALL;

  info_.rate = static_cast<std::uint32_t>(codec_->sample_rate);
  info_.channels = static_cast<std::size_t>(codec_->ch_layout.nb_channels);
  if (format_->duration != AV_NOPTS_VALUE && format_->duration > 0)
    info_.durationMs = static_cast<std::uint64_t>(format_->duration / 1000);
  const bool hls = std::string_view(format_->iformat->name) == "hls";
  const bool seeks = hls || (format_->pb != nullptr && (format_->pb->seekable & AVIO_SEEKABLE_NORMAL) != 0);
  info_.seekable = info_.durationMs > 0 && seeks;
  info_.tags = {tag(format_, stream, "title"), tag(format_, stream, "artist"), tag(format_, stream, "album"), {}, {}, {}};
  return std::nullopt;
}

Result<Decoded, std::string_view> UrlInput::next() {
  for (;;) {
    const int received = avcodec_receive_frame(codec_, frame_);
    if (received == 0) {
      const auto channels = static_cast<std::size_t>(frame_->ch_layout.nb_channels);
      const auto frames = static_cast<std::size_t>(frame_->nb_samples);
      const auto format = static_cast<AVSampleFormat>(frame_->format);
      const auto rate = static_cast<std::uint32_t>(frame_->sample_rate);
      const bool converted =
          channels >= 1 && channels <= 2 &&
          toInterleavedFloat(format, frame_->extended_data, channels, frames, samples_);
      av_frame_unref(frame_);
      if (!converted)
        return kUrlUnsupported;
      return Decoded{std::span<const float>(samples_), rate, channels, false};
    }
    if (received == AVERROR_EOF)
      return Decoded{{}, info_.rate, info_.channels, true};
    // a damaged frame is skipped like a damaged packet
    if (received != AVERROR(EAGAIN) && received != AVERROR_INVALIDDATA)
      return urlCode(received, false);
    if (received == AVERROR_INVALIDDATA)
      continue;

    const int read = av_read_frame(format_, packet_);
    if (read == AVERROR_EOF) {
      // the decoder's last frames, then its end
      if (avcodec_send_packet(codec_, nullptr) < 0)
        return Decoded{{}, info_.rate, info_.channels, true};
      continue;
    }
    if (read < 0)
      return urlCode(read, false);
    const bool ours = packet_->stream_index == stream_;
    const int sent = ours ? avcodec_send_packet(codec_, packet_) : 0;
    av_packet_unref(packet_);
    if (sent < 0 && sent != AVERROR_INVALIDDATA && sent != AVERROR(EAGAIN))
      return urlCode(sent, false);
  }
}

bool UrlInput::seek(std::uint64_t ms) {
  const auto target = static_cast<std::int64_t>(ms) * 1000;
  if (avformat_seek_file(format_, -1, INT64_MIN, target, target, 0) < 0)
    return false;
  avcodec_flush_buffers(codec_);
  return true;
}

std::optional<std::string> UrlInput::icyPacket() const {
  if (format_->pb == nullptr)
    return std::nullopt;
  std::uint8_t* packet = nullptr;
  if (av_opt_get(format_->pb, "icy_metadata_packet", AV_OPT_SEARCH_CHILDREN, &packet) < 0 || packet == nullptr)
    return std::nullopt;
  std::string text(reinterpret_cast<const char*>(packet));
  av_free(packet);
  if (text.empty())
    return std::nullopt;
  return text;
}

int UrlInput::interrupted(void* self) { return static_cast<UrlInput*>(self)->interrupt_() ? 1 : 0; }

int UrlInput::openNested(AVFormatContext* format, AVIOContext** pb, const char* url, int flags,
                         AVDictionary** options) {
  auto* self = static_cast<UrlInput*>(format->opaque);
  AVDictionary* own = nullptr;
  AVDictionary** given = options != nullptr ? options : &own;
  for (const auto& option : self->options_)
    if (!formatOption(option.key))
      av_dict_set(given, option.key.c_str(), option.value.c_str(), AV_DICT_DONT_OVERWRITE);
  const int opened = self->defaultOpen_(format, pb, url, flags, given);
  av_dict_free(&own);
  return opened;
}

}  // namespace cadence::url

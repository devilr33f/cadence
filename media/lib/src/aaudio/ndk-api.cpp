#include "aaudio/stream.h"

#include <aaudio/AAudio.h>

#include <ctime>
#include <string>
#include <utility>

namespace cadence::aaudio {

namespace {

class NdkStream final : public Stream {
 public:
  NdkStream(DataCallback data, ErrorCallback error) : data_(std::move(data)), error_(std::move(error)) {}
  ~NdkStream() override {
    if (stream_ != nullptr) {
      AAudioStream_requestStop(stream_);
      AAudioStream_close(stream_);
    }
  }
  NdkStream(const NdkStream&) = delete;
  NdkStream& operator=(const NdkStream&) = delete;

  aaudio_result_t open(const StreamRequest& request) {
    AAudioStreamBuilder* b = nullptr;
    if (const auto r = AAudio_createStreamBuilder(&b); r != AAUDIO_OK)
      return r;
    const bool output = request.direction == Direction::Output;
    AAudioStreamBuilder_setDirection(b, output ? AAUDIO_DIRECTION_OUTPUT : AAUDIO_DIRECTION_INPUT);
    AAudioStreamBuilder_setSharingMode(b, AAUDIO_SHARING_MODE_SHARED);
    AAudioStreamBuilder_setFormat(b, AAUDIO_FORMAT_PCM_I16);
    AAudioStreamBuilder_setChannelCount(b, request.channels);
    AAudioStreamBuilder_setSampleRate(b, request.sampleRate);
    AAudioStreamBuilder_setPerformanceMode(
        b, request.lowLatency ? AAUDIO_PERFORMANCE_MODE_LOW_LATENCY : AAUDIO_PERFORMANCE_MODE_NONE);
    if (output) {
      AAudioStreamBuilder_setUsage(b, AAUDIO_USAGE_MEDIA);
      AAudioStreamBuilder_setContentType(b, AAUDIO_CONTENT_TYPE_MUSIC);
    } else {
      AAudioStreamBuilder_setInputPreset(b, AAUDIO_INPUT_PRESET_VOICE_RECOGNITION);
    }
    AAudioStreamBuilder_setDataCallback(b, &NdkStream::onData, this);
    AAudioStreamBuilder_setErrorCallback(b, &NdkStream::onError, this);
    const auto r = AAudioStreamBuilder_openStream(b, &stream_);
    AAudioStreamBuilder_delete(b);
    if (r != AAUDIO_OK)
      stream_ = nullptr;
    channels_ = r == AAUDIO_OK ? AAudioStream_getChannelCount(stream_) : 0;
    return r;
  }

  [[nodiscard]] StreamInfo info() const override {
    StreamInfo i;
    i.sampleRate = AAudioStream_getSampleRate(stream_);
    i.channels = AAudioStream_getChannelCount(stream_);
    i.framesPerBurst = AAudioStream_getFramesPerBurst(stream_);
    i.bufferFrames = AAudioStream_getBufferSizeInFrames(stream_);
    i.lowLatency = AAudioStream_getPerformanceMode(stream_) == AAUDIO_PERFORMANCE_MODE_LOW_LATENCY;
    return i;
  }
  bool start() override { return AAudioStream_requestStart(stream_) == AAUDIO_OK; }
  void stop() override {
    AAudioStream_requestStop(stream_);
    // requestStop is asynchronous: wait for the callbacks to end
    aaudio_stream_state_t next = AAUDIO_STREAM_STATE_UNINITIALIZED;
    AAudioStream_waitForStateChange(stream_, AAUDIO_STREAM_STATE_STOPPING, &next, 500'000'000LL);
  }
  [[nodiscard]] std::int64_t framesWritten() const override { return AAudioStream_getFramesWritten(stream_); }
  [[nodiscard]] std::int64_t framesRead() const override { return AAudioStream_getFramesRead(stream_); }
  [[nodiscard]] std::optional<Timestamp> timestamp() const override {
    Timestamp t;
    if (AAudioStream_getTimestamp(stream_, CLOCK_MONOTONIC, &t.framePosition, &t.monotonicNs) != AAUDIO_OK)
      return std::nullopt;
    return t;
  }
  [[nodiscard]] std::int32_t xruns() const override { return AAudioStream_getXRunCount(stream_); }

 private:
  static aaudio_data_callback_result_t onData(AAudioStream*, void* user, void* audio, std::int32_t frames) {
    auto* self = static_cast<NdkStream*>(user);
    self->data_(std::span<std::int16_t>(static_cast<std::int16_t*>(audio),
                                        static_cast<std::size_t>(frames) * static_cast<std::size_t>(self->channels_)));
    return AAUDIO_CALLBACK_RESULT_CONTINUE;
  }
  static void onError(AAudioStream*, void* user, aaudio_result_t error) {
    static_cast<NdkStream*>(user)->error_(static_cast<int>(error));
  }

  DataCallback data_;
  ErrorCallback error_;
  AAudioStream* stream_ = nullptr;
  int channels_ = 0;
};

class NdkApi final : public Api {
 public:
  Result<std::unique_ptr<Stream>, std::string> open(const StreamRequest& request, DataCallback data,
                                                    ErrorCallback error) override {
    auto stream = std::make_unique<NdkStream>(std::move(data), std::move(error));
    if (const auto r = stream->open(request); r != AAUDIO_OK)
      return std::string(AAudio_convertResultToText(r));
    return std::unique_ptr<Stream>(std::move(stream));
  }
  [[nodiscard]] std::int64_t nowNs() const override {
    timespec ts{};
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return static_cast<std::int64_t>(ts.tv_sec) * 1'000'000'000LL + ts.tv_nsec;
  }
};

}  // namespace

Api& ndkApi() {
  static NdkApi api;
  return api;
}

}  // namespace cadence::aaudio

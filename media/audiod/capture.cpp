#include "audiod/capture.h"

#include "audio/opus-codec.h"
#include "audiod/mic-layout.h"
#include "audiod/sparse.h"
#include "audiod/stream-recorder.h"
#include "file/file-capture.h"
#include "sync/clock-socket.h"

#ifdef CADENCE_MEDIA_ALSA
#include "capture/alsa-capture.h"
#endif
#ifdef CADENCE_MEDIA_AAUDIO
#include "aaudio/aaudio-capture.h"
#endif

#include <algorithm>
#include <chrono>
#include <cstring>
#include <span>
#include <system_error>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::chrono::milliseconds kReadTimeout(20);
// a calibration's window: anything longer is refused
constexpr std::int64_t kMaxRecordingNs = sync::kMaxRecordingNs;
constexpr std::uint64_t kEncodeFailureLimit = 5;
// past four microphones the analysis costs more than it gains, especially on a 32-bit arm
constexpr std::size_t kMaxEngineRecordingChannels = 4;
constexpr std::string_view kEngine = "stock";
// the stock spotter confirms a hit; it gives no score
constexpr float kConfirmed = 1.0f;

// the file backend's wav: 16 kHz mono, read a frame at a time
const CaptureLayout kFileLayout{.device = {},
                                .sampleRate = kOpusSampleRate,
                                .channels = 1,
                                .microphones = {0},
                                .speakers = {},
                                .periodFrames = kEncoderFrameSamples,
                                .bufferFrames = kEncoderFrameSamples * 4};

std::string_view token(CaptureErrorCode code) {
  switch (code) {
    case CaptureErrorCode::OpenFailed:
      return "open";
    case CaptureErrorCode::ConfigureFailed:
      return "configure";
    case CaptureErrorCode::FormatMismatch:
      return "format";
    default:
      return "device";
  }
}

std::string_view stage(AudioEngineErrorCode code) {
  switch (code) {
    case AudioEngineErrorCode::StockFailed:
      return "stock";
    case AudioEngineErrorCode::CaptureOpenFailed:
      return "open";
    case AudioEngineErrorCode::CaptureFormatMismatch:
      return "format";
    default:
      return "engine";
  }
}

bool fileFormat(const CaptureConfig& config) {
  return (config.sample_rate == 0 || config.sample_rate == kOpusSampleRate) &&
         (config.channels == 0 || config.channels == 1);
}

// printable ascii that fits CaptureFailed, so cadenced reads it back exactly as it sent it; anything else may
// not even be the utf-8 a proto string must be
bool echoable(std::string_view id) {
  return !id.empty() && id.size() < sizeof(cadence_audiod_v1_CaptureFailed::session_id) &&
         std::all_of(id.begin(), id.end(), [](char c) { return c >= 0x20 && c <= 0x7e; });
}

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto size = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), size);
  out[size] = '\0';
}

}  // namespace

Result<std::unique_ptr<PcmSource>, std::string_view> openCapture(const CaptureConfig& config) {
  switch (config.backend) {
    case cadence_audiod_v1_Backend_BACKEND_FILE:
      if (!fileFormat(config))
        return std::string_view("format");
      if (config.file_path[0] == '\0')
        return std::string_view("file_path");
      return std::unique_ptr<PcmSource>(std::make_unique<FileCapture>(config.file_path));
#ifdef CADENCE_MEDIA_ALSA
    case cadence_audiod_v1_Backend_BACKEND_ALSA:
      if (config.device[0] == '\0')
        return std::string_view("device");
      return std::unique_ptr<PcmSource>(std::make_unique<AlsaCapture>(config.device));
#endif
#ifdef CADENCE_MEDIA_AAUDIO
    case cadence_audiod_v1_Backend_BACKEND_AAUDIO:
      return std::unique_ptr<PcmSource>(std::make_unique<AAudioCapture>(aaudio::ndkApi()));
#endif
    default:
      return std::string_view("backend");
  }
}

Result<CaptureLayout, std::string_view> captureLayout(const CaptureConfig& config) {
  if (config.backend == cadence_audiod_v1_Backend_BACKEND_FILE)
    return kFileLayout;
  return boardLayout(config);
}

Capture::Capture(Loop& loop, IpcServer& ipc, const Log& log, CaptureFactory open, VendorOptions vendor)
    : loop_(loop),
      ipc_(ipc),
      log_(log),
      open_(std::move(open)),
      vendor_(std::move(vendor)),
      resolver_(loop, log, vendor_) {
  ipc_.on(cadence_audiod_v1_Envelope_capture_start_tag,
          [this](const Envelope& envelope) { start(envelope.body.capture_start); });
  ipc_.on(cadence_audiod_v1_Envelope_capture_stop_tag,
          [this](const Envelope& envelope) { stop(envelope.body.capture_stop); });
  ipc_.on(cadence_audiod_v1_Envelope_mute_state_tag,
          [this](const Envelope& envelope) { mute(envelope.body.mute_state); });
  ipc_.on(cadence_audiod_v1_Envelope_wake_listen_tag,
          [this](const Envelope& envelope) { listen(envelope.body.wake_listen); });
}

Capture::~Capture() { teardown(); }

void Capture::configure(const Configure& configuration) {
  teardown();
  config_.reset();
  if (!configuration.has_capture)
    return;
  config_ = configuration.capture;
  const bool alsa = config_->backend == cadence_audiod_v1_Backend_BACKEND_ALSA;
  const bool file = config_->backend == cadence_audiod_v1_Backend_BACKEND_FILE;
  if (config_->has_keyword && (alsa || file)) {
#ifdef CADENCE_MEDIA_KWS
    startKeyword();
#else
    mode_ = Mode::Raw;
    report({.engines = false, .code = "keyword_unsupported"});
#endif
    return;
  }
  if (alsa && config_->has_engines && config_->engines.enabled) {
    mode_ = Mode::Resolving;
    resolver_.resolve(*config_, [this](VendorVerdict verdict) { resolved(std::move(verdict)); });
    return;
  }
  mode_ = Mode::Raw;
  if (alsa)
    report({.engines = false, .code = "disabled"});
  settleListen();
}

// after the parts' own ready, so the status follows the media state cadenced reads right after Ready
void Capture::ready() {
  readied_ = true;
  if (const auto status = std::exchange(held_, std::nullopt))
    loop_.post([this, epoch = epoch_, status = *status] {
      if (epoch == epoch_)
        sendStatus(status);
    });
}

void Capture::reset() {
  teardown();
  config_.reset();
  muted_.store(false);
  listen_ = false;
}

void Capture::teardown() {
  finishRecording();
  resolver_.cancel();
  end();
  disarmStartWatch();
  // a start that hangs holds the loop here: cadenced's health ping then restarts audiod
  if (starting_.joinable())
    starting_.join();
  if (engine_)
    stopEngines();
#ifdef CADENCE_MEDIA_KWS
  if (keyword_)
    stopKeyword();
#endif
  if (listening_)
    stopListen();
  lastWake_.reset();
  unknownCommands_ = 0;
  mode_ = Mode::Off;
  ++epoch_;
  readied_ = false;
  held_.reset();
}

void Capture::start(const cadence_audiod_v1_CaptureStart& request) {
  const std::string_view id = request.session_id;
  // a newer start supersedes the running session even when this one is refused
  end();
  traceId_ = std::string(request.has_trace ? trace::traceId(request.trace.traceparent) : std::string_view{});
  prerollFrom_ = request.preroll_from_ms;
  if (!config_)
    return refuse(id, "unconfigured");
  if (!safeStreamId(id))
    return refuse(id, "session_id");
  begin(id);
}

void Capture::begin(std::string_view id) {
  if (mode_ == Mode::Resolving) {
    pending_ = std::string(id);
    log_.info("capture_pending");
    return;
  }
  sessionId_ = std::string(id);
  seq_ = 0;
  dropped_ = 0;
  bytes_ = 0;
  firstSent_ = false;
  preroll_ = false;
  startedNs_ = trace::unixNowNs();
  const auto generation = ++generation_;
  if (mode_ == Mode::Engines || mode_ == Mode::Keyword || mode_ == Mode::Listen) {
    streaming_ = true;
    preroll_ = prerollFrom_ > 0 || (lastWake_ && std::chrono::steady_clock::now() - *lastWake_ < vendor_.wakePreroll);
    lastWake_.reset();
    if (traces_ != nullptr && trace::kEnabled && engine_)
      enginesAtStart_ = engine_->status();
    openStream(generation, preroll_);
  } else {
    runRaw(generation);
  }
  mark(trace::Stage::CaptureStarted, {}, startedNs_);
  if (traceId_.empty())
    log_.info("capture_started");
  else
    log_.info("capture_started", {{"trace", traceId_}});
}

void Capture::openStream(std::uint64_t generation, bool preroll) {
  if (engine_)
    engine_->open(generation, preroll, prerollFrom_);
#ifdef CADENCE_MEDIA_KWS
  if (keyword_)
    keyword_->open(generation, preroll, prerollFrom_);
#endif
  if (listening_)
    listening_->open(generation, prerollFrom_);
}

void Capture::closeStream() {
  if (engine_)
    engine_->close();
#ifdef CADENCE_MEDIA_KWS
  if (keyword_)
    keyword_->close();
#endif
  if (listening_)
    listening_->close();
}

void Capture::runRaw(std::uint64_t generation) {
  auto stopped = std::make_shared<std::atomic<bool>>(false);
  running_.emplace(Running{sessionId_, stopped, std::thread([this, generation, config = *config_, stopped] {
                             run(generation, config, *stopped);
                           })});
}

void Capture::beginPending() {
  if (pending_.empty())
    return;
  const auto id = std::exchange(pending_, {});
  begin(id);
}

void Capture::stop(const cadence_audiod_v1_CaptureStop& request) {
  const std::string_view id = request.session_id;
  if ((running_ && running_->id == id) || (streaming_ && sessionId_ == id) || (!pending_.empty() && pending_ == id))
    end();
}

void Capture::mute(const cadence_audiod_v1_MuteState& request) {
  muted_.store(request.muted);
  if (request.muted)
    cancelNative();
  if (engine_)
    engine_->setMuted(request.muted);
#ifdef CADENCE_MEDIA_KWS
  if (keyword_)
    keyword_->setMuted(request.muted);
#endif
  if (listening_)
    listening_->setMuted(request.muted);
  log_.info("capture_muted", {{"muted", request.muted ? 1 : 0}});
}

void Capture::listen(const cadence_audiod_v1_WakeListen& request) {
  if (listen_ == request.enabled)
    return;
  listen_ = request.enabled;
  if (engine_)
    engine_->setListen(listen_);
#ifdef CADENCE_MEDIA_KWS
  if (keyword_)
    keyword_->setListen(listen_);
#endif
  log_.info("capture_listen", {{"enabled", listen_ ? 1 : 0}});
  settleListen();
}

void Capture::settleListen() {
  if (!config_)
    return;
  if (mode_ == Mode::Raw && listen_ && !running_ && !recorder_)
    startListen();
  else if (mode_ == Mode::Listen && !listen_ && !streaming_)
    stopListen();
}

void Capture::startListen() {
  const auto layout = captureLayout(*config_);
  if (!layout.ok()) {
    log_.warn("capture_listen_failed", {{"stage", "layout"}});
    return;
  }
  auto source = open_(*config_);
  if (!source.ok()) {
    log_.warn("capture_listen_failed", {{"stage", "source"}, {"reason", source.error()}});
    return;
  }
  const auto epoch = epoch_;
  listening_ = std::make_unique<ListenCapture>(
      std::move(source.value()),
      ListenEvents{.pcm = pcmSink(epoch), .packet = packetSink(), .failed = failureSink()}, log_);
  listening_->setMuted(muted_.load());
  if (const auto failed = listening_->start(layout.value())) {
    log_.warn("capture_listen_failed", {{"stage", *failed}});
    listening_.reset();
    return;
  }
  mode_ = Mode::Listen;
  log_.info("capture_listen_started");
}

void Capture::stopListen() {
  listening_->stop();
  const auto stats = listening_->stats();
  log_.info("capture_listen_stopped", {{"frames", stats.frames},
                                       {"dropped", pcmDropped_},
                                       {"recoveries", stats.recoveries},
                                       {"read_errors", stats.readErrors}});
  listening_.reset();
  pcmDropped_ = 0;
  if (mode_ == Mode::Listen)
    mode_ = Mode::Raw;
}

std::function<void(std::uint64_t, std::int64_t, std::vector<std::int16_t>, bool)> Capture::pcmSink(
    std::uint64_t epoch) {
  return [this, epoch](std::uint64_t seq, std::int64_t atMs, std::vector<std::int16_t> pcm, bool gap) {
    loop_.post([this, epoch, seq, atMs, pcm = std::move(pcm), gap] {
      if (epoch == epoch_ && listen_ && !streaming_ && !muted_.load())
        sendPcm(seq, atMs, pcm, gap);
    });
  };
}

void Capture::sendPcm(std::uint64_t seq, std::int64_t atMs, const std::vector<std::int16_t>& pcm, bool gap) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_mic_pcm_tag;
  auto& mic = envelope.body.mic_pcm;
  mic.seq = seq;
  mic.at_ms = atMs;
  mic.gap = gap;
  const auto bytes = std::min(pcm.size() * sizeof(std::int16_t), sizeof mic.pcm.bytes);
  // s16le on the wire: every board audiod runs on is little-endian
  std::memcpy(mic.pcm.bytes, pcm.data(), bytes);
  mic.pcm.size = static_cast<pb_size_t>(bytes);
  // a client that stopped reading loses frames, never the loop; its detector sees the gap in seq
  if (!ipc_.send(envelope) && sparse(++pcmDropped_))
    log_.warn("mic_pcm_dropped", {{"count", pcmDropped_}});
}

void Capture::end() {
  pending_.clear();
  if (streaming_) {
    closeStream();
    streaming_ = false;
    if (traces_ != nullptr && trace::kEnabled && engine_) {
      const auto status = engine_->status();
      mark(trace::Stage::VqeStats,
           {{"chunks", static_cast<double>(status.chunks - enginesAtStart_.chunks)},
            {"gaps", static_cast<double>(status.gaps - enginesAtStart_.gaps)},
            {"vqe_errors", static_cast<double>(status.vqeErrors - enginesAtStart_.vqeErrors)}},
           startedNs_, trace::unixNowNs() - startedNs_);
    }
  } else if (running_) {
    running_->stop->store(true);
    running_->thread.join();
    running_.reset();
  } else {
    return;
  }
  // packets posted before the session ended carry the old generation
  ++generation_;
  const auto now = trace::unixNowNs();
  mark(trace::Stage::CaptureStats,
       {{"frames", static_cast<double>(seq_)}, {"bytes", static_cast<double>(bytes_)},
        {"dropped", static_cast<double>(dropped_)}},
       startedNs_, now - startedNs_);
  mark(trace::Stage::CaptureStopped, {}, now);
  if (traceId_.empty())
    log_.info("capture_stopped", {{"frames", seq_}, {"dropped", dropped_}});
  else
    log_.info("capture_stopped", {{"frames", seq_}, {"dropped", dropped_}, {"trace", traceId_}});
  prerollFrom_ = 0;
  settleListen();
}

void Capture::resolved(VendorVerdict verdict) {
  if (!verdict.setup)
    return fallBack(verdict.code);
  startEngines(std::move(*verdict.setup), verdict.code);
}

void Capture::startEngines(VendorSetup setup, std::string_view code) {
  auto source = open_(*config_);
  if (!source.ok()) {
    log_.warn("capture_engines_failed", {{"stage", "source"}, {"reason", source.error()}});
    return fallBack("start_failed");
  }
  const bool commands = setup.stock.commandWords;
  const auto& configured = config_->engines;
  CommandWords words(commands ? std::vector<std::string>(configured.command_words,
                                                         configured.command_words + configured.command_words_count)
                              : std::vector<std::string>{});
  const auto epoch = epoch_;
  engine_ = std::make_unique<CaptureEngine>(std::move(source.value()), events(epoch), std::move(words), vendor_.loader);
  engine_->setMuted(muted_.load());
  engine_->setListen(listen_);
  // the stock library may take a while to build its engines: the loop goes on meanwhile
  try {
    starting_ = std::thread([this, engine = engine_.get(), config = AudioEngineConfig{std::move(setup.stock)}, epoch,
                             code, commands] {
      auto started = engine->start(config);
      loop_.post([this, epoch, started = std::move(started), code, commands] {
        enginesStarted(epoch, started, code, commands);
      });
    });
  } catch (const std::system_error& error) {
    log_.warn("capture_engines_failed", {{"stage", "thread"}, {"errno", error.code().value()}});
    engine_.reset();
    return fallBack("start_failed");
  }
  startWatch_ = loop_.after(vendor_.startLimit, [this, epoch] {
    startWatch_.reset();
    if (epoch == epoch_)
      enginesHung();
  });
}

void Capture::enginesHung() {
  log_.error("capture_engines_hung", {{"limit_ms", static_cast<std::int64_t>(vendor_.startLimit.count())}});
  vendor_.exit(kExitEnginesHung);
}

void Capture::disarmStartWatch() {
  if (const auto armed = std::exchange(startWatch_, std::nullopt))
    loop_.cancel(*armed);
}

void Capture::enginesStarted(std::uint64_t epoch, const Result<AudioEngineInfo, AudioEngineError>& started,
                             std::string_view code, bool commands) {
  if (epoch != epoch_)
    return;
  disarmStartWatch();
  starting_.join();
  if (!started.ok()) {
    log_.warn("capture_engines_failed", {{"stage", stage(started.error().code)}});
    engine_.reset();
    return fallBack("start_failed");
  }
  const auto& stock = started.value().stock;
  const bool listening = stock.spotter && stock.spotter->commands;
  if (stock.spotterError)
    code = "spotter_failed";
  else if (commands && !listening)
    code = "command_model_failed";
  mode_ = Mode::Engines;
  log_.info("capture_engines_started", {{"commands", listening ? 1 : 0}});
  report({.engines = true, .code = code});
  beginPending();
}

void Capture::stopEngines() {
  const auto status = engine_->status();
  log_.info("capture_engines_stopped", {{"chunks", status.chunks},
                                        {"wakes", status.wakes},
                                        {"suppressed_wakes", status.suppressedWakes},
                                        {"commands", status.commandHits},
                                        {"unknown_commands", unknownCommands_},
                                        {"gaps", status.gaps},
                                        {"vqe_errors", status.vqeErrors},
                                        {"spotter_errors", status.spotterErrors},
                                        {"spotter_rebuilds", status.spotterRebuilds},
                                        {"spotter_rebuild_failures", status.spotterRebuildFailures},
                                        {"capture_errors", status.captureErrors}});
  engine_.reset();
}

void Capture::fallBack(std::string_view code) {
  mode_ = Mode::Raw;
  report({.engines = false, .code = code});
  settleListen();
  beginPending();
}

EngineEvents Capture::events(std::uint64_t epoch) {
  return {
      .wake =
          [this, epoch](std::int64_t atUnixNs) {
            loop_.post([this, epoch, atUnixNs] {
              if (epoch == epoch_)
                sendWake(kEngine, kConfirmed, atUnixNs);
            });
          },
      .command =
          [this, epoch](std::uint32_t id, std::int64_t atMs) {
            loop_.post([this, epoch, id, atMs] {
              if (epoch == epoch_)
                sendCommand(id, atMs);
            });
          },
      .unknownCommand =
          [this, epoch] {
            loop_.post([this, epoch] {
              if (epoch == epoch_)
                unknownCommand();
            });
          },
      .packet = packetSink(),
      .failed = failureSink(),
      .pcm = pcmSink(epoch),
  };
}

std::function<void(std::uint64_t, std::vector<std::uint8_t>)> Capture::packetSink() {
  return [this](std::uint64_t session, std::vector<std::uint8_t> opus) {
    loop_.post([this, session, opus = std::move(opus)] { send(session, opus); });
  };
}

std::function<void(std::uint64_t)> Capture::failureSink() {
  return [this](std::uint64_t session) {
    loop_.post([this, session] {
      log_.warn("capture_failed", {{"stage", "encoder"}});
      fail(session, "encode_failed");
    });
  };
}

void Capture::unknownCommand() {
  if (sparse(++unknownCommands_))
    log_.info("command_unknown", {{"count", unknownCommands_}});
}

void Capture::report(Status status) {
  log_.info("vendor_status", {{"engines", status.engines ? 1 : 0}, {"code", status.code}});
  if (readied_)
    sendStatus(status);
  else
    held_ = status;
}

void Capture::sendStatus(Status status) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_vendor_status_tag;
  envelope.body.vendor_status.engines = status.engines;
  copyText(envelope.body.vendor_status.code, status.code);
  ipc_.send(envelope);
}

void Capture::sendWake(std::string_view engine, float score, std::int64_t atUnixNs) {
  // the mark first: cadenced takes it when the wake's turn opens
  if (traces_ != nullptr)
    traces_->mark(trace::Stage::WakeDetected, trace::Target::None, {}, {{"score", score}}, atUnixNs);
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_wake_event_tag;
  auto& wake = envelope.body.wake_event;
  copyText(wake.engine, engine);
  wake.score = score;
  wake.at_ms = atUnixNs / 1'000'000;
  wake.at_unix_ns = atUnixNs;
  ipc_.send(envelope);
  lastWake_ = std::chrono::steady_clock::now();
  log_.info("wake_heard", {{"engine", engine}});
}

void Capture::sendCommand(std::uint32_t id, std::int64_t atMs) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_command_word_tag;
  auto& command = envelope.body.command_word;
  command.id = id;
  command.score = kConfirmed;
  command.at_ms = atMs;
  ipc_.send(envelope);
  log_.info("command_heard", {{"id", id}});
}

void Capture::send(std::uint64_t generation, const std::vector<std::uint8_t>& opus) {
  if (generation != generation_)
    return;
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_mic_opus_tag;
  auto& mic = envelope.body.mic_opus;
  std::memcpy(mic.session_id, sessionId_.data(), std::min(sessionId_.size(), sizeof mic.session_id - 1));
  mic.seq = seq_++;
  bool sent = false;
  if (opus.size() <= sizeof mic.opus.bytes) {
    std::memcpy(mic.opus.bytes, opus.data(), opus.size());
    mic.opus.size = static_cast<pb_size_t>(opus.size());
    sent = ipc_.send(envelope);
  }
  if (!sent && sparse(++dropped_))
    log_.warn("mic_dropped", {{"count", dropped_}});
  if (!sent)
    return;
  bytes_ += opus.size();
  if (!firstSent_) {
    firstSent_ = true;
    const auto now = trace::unixNowNs();
    mark(trace::Stage::FirstOpusFrame, {{"preroll", preroll_ ? 1.0 : 0.0}}, startedNs_, now - startedNs_);
  }
}

void Capture::mark(trace::Stage stage, std::initializer_list<trace::Value> values, std::int64_t atUnixNs,
                   std::int64_t durationNs) {
  if (traces_ != nullptr)
    traces_->mark(stage, trace::Target::Session, sessionId_, values, atUnixNs, durationNs);
}

void Capture::refuse(std::string_view id, std::string_view reason) {
  log_.warn("capture_refused", {{"reason", reason}});
  if (echoable(id))
    sendFailed(id, reason);
}

void Capture::fail(std::uint64_t generation, std::string_view reason) {
  // a superseded or ended session's failure is not cadenced's business
  if (generation != generation_)
    return;
  sendFailed(sessionId_, reason);
}

void Capture::sendFailed(std::string_view id, std::string_view reason) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_capture_failed_tag;
  auto& failed = envelope.body.capture_failed;
  std::memcpy(failed.session_id, id.data(), std::min(id.size(), sizeof failed.session_id - 1));
  std::memcpy(failed.reason, reason.data(), std::min(reason.size(), sizeof failed.reason - 1));
  ipc_.send(envelope);
}

void Capture::record(std::int64_t fromMono, std::int64_t toMono, std::function<void(sync::MicResult)> done) {
  loop_.post([this, fromMono, toMono, done = std::move(done)]() mutable {
    startRecording(fromMono, toMono, std::move(done), false);
  });
}

void Capture::recordNative(std::int64_t fromMono, std::int64_t toMono, std::function<void(sync::MicResult)> done) {
  loop_.post([this, fromMono, toMono, done = std::move(done)]() mutable {
    startRecording(fromMono, toMono, std::move(done), true);
  });
}

void Capture::cancelNative() {
  loop_.post([this] {
    if (nativeRecording_)
      finishRecording();
  });
}

void Capture::startRecording(std::int64_t fromMono, std::int64_t toMono, std::function<void(sync::MicResult)> done,
                             bool native) {
  const auto refuse = [&](std::string_view code) {
    nativeRecording_ = false;
    log_.info("calibration_record_refused", {{"code", code}});
    done(sync::MicResult{std::string(code), {}});
  };
  const auto now = sync::monoNs();
  if (!config_ || tapped_ || recorder_ || toMono <= now || toMono - fromMono > kMaxRecordingNs ||
      (mode_ != Mode::Raw && mode_ != Mode::Engines))
    return refuse("no_capture");
  if (muted_.load())
    return refuse("muted");
  nativeRecording_ = native;
  if (mode_ == Mode::Raw) {
    if (running_)
      return refuse("capturing");
    auto stopped = std::make_shared<std::atomic<bool>>(false);
    recorder_.emplace(Running{"calibration", stopped,
                              std::thread([this, config = *config_, fromMono, toMono, done = std::move(done), stopped,
                                          native] {
                                recordRaw(config, fromMono, toMono, done, *stopped, native);
                              })});
    // the thread hands the recording over itself; the loop joins it once the window is past
    tapTimer_ = loop_.after(std::chrono::nanoseconds(toMono - now) + std::chrono::milliseconds(200), [this] {
      tapTimer_.reset();
      finishRecording();
    });
    return;
  }
  auto tapped = std::make_shared<Tapped>();
  tapped->fromMono = fromMono;
  tapped->toMono = toMono;
  // the native tap stays at 48 khz for ultrasound; the legacy recording alone is resampled to 16 khz
  const auto tapRate = engine_->tapRate();
  const auto inputChannels = engine_->tapChannels();
  const auto rate = native ? static_cast<double>(tapRate) : sync::kCalibrationMicRate;
  const auto channels = sync::recordingChannels(std::min(inputChannels, kMaxEngineRecordingChannels),
                                                toMono - fromMono + 300'000'000, rate);
  CaptureLayout tapLayout{.device = {},
                          .sampleRate = tapRate,
                          .channels = inputChannels,
                          .microphones = {},
                          .speakers = {},
                          .periodFrames = 0,
                          .bufferFrames = 0};
  for (std::size_t c = 0; c < std::min(inputChannels, kMaxEngineRecordingChannels); ++c)
    tapLayout.microphones.push_back(c);
  auto split = MicSplit::create(tapLayout, channels, static_cast<std::uint32_t>(rate));
  if (!split.ok())
    return refuse("no_capture");
  auto resample = std::make_shared<MicSplit>(std::move(split.value()));
  tapped->recording.rate = rate;
  tapped->recording.channels = channels;
  tapped->recording.pcm.reserve(channels * (static_cast<std::size_t>(static_cast<double>(toMono - fromMono) * rate / 1e9) +
                                            static_cast<std::size_t>(rate / 5)));
  tapped_ = tapped;
  tappedDone_ = std::move(done);
  engine_->setTap([tapped, muted = &muted_, resample](std::span<const std::int16_t> pcm, std::size_t given,
                                                      std::int64_t mono) {
    std::lock_guard lock(tapped->mutex);
    if (mono > tapped->toMono + 100'000'000 || given == 0)
      return;
    if (mono >= tapped->fromMono && muted->load())
      tapped->muted = true;
    tapped->converted.clear();
    resample->push(pcm, tapped->converted);
    if (mono < tapped->fromMono)
      return;
    auto& rec = tapped->recording;
    rec.pcm.insert(rec.pcm.end(), tapped->converted.begin(), tapped->converted.end());
    // endFrame is in the recording's output-rate frames, not the tap's input frames
    rec.stamps.push_back({rec.frames(), mono});
  });
  tapTimer_ = loop_.after(std::chrono::nanoseconds(toMono - now) + std::chrono::milliseconds(200), [this] {
    tapTimer_.reset();
    finishRecording();
  });
  log_.info("calibration_record", {{"mode", "engines"},
                                   {"ms", (toMono - fromMono) / 1'000'000},
                                   {"rate", static_cast<std::int64_t>(rate)},
                                   {"channels", static_cast<std::uint64_t>(channels)}});
}

void Capture::finishRecording() {
  if (const auto armed = std::exchange(tapTimer_, std::nullopt))
    loop_.cancel(*armed);
  if (recorder_) {
    // a thread stopped before the window's end hands over a refusal itself
    recorder_->stop->store(true);
    recorder_->thread.join();
    recorder_.reset();
  }
  nativeRecording_ = false;
  if (!tapped_)
    return;
  if (engine_)
    engine_->setTap({});
  sync::MicResult result;
  {
    std::lock_guard lock(tapped_->mutex);
    const bool whole = sync::monoNs() >= tapped_->toMono;
    if (tapped_->muted || muted_.load())
      result.code = "muted";
    else if (!whole || tapped_->recording.pcm.empty())
      result.code = "no_capture";
    else
      result.recording = std::move(tapped_->recording);
  }
  log_.info("calibration_recorded", {{"code", result.code.empty() ? std::string_view("ok") : result.code},
                                     {"frames", static_cast<std::uint64_t>(result.recording.frames())},
                                     {"channels", static_cast<std::uint64_t>(result.recording.channels)}});
  tapped_.reset();
  auto done = std::exchange(tappedDone_, {});
  if (done)
    done(std::move(result));
}

void Capture::recordRaw(const CaptureConfig& config, std::int64_t fromMono, std::int64_t toMono,
                        std::function<void(sync::MicResult)> handOver, const std::atomic<bool>& stopped, bool native) {
  const auto done = [&](sync::MicResult result) {
    log_.info("calibration_recorded", {{"code", result.code.empty() ? std::string_view("ok") : result.code},
                                       {"frames", static_cast<std::uint64_t>(result.recording.frames())},
                                       {"channels", static_cast<std::uint64_t>(result.recording.channels)}});
    handOver(std::move(result));
  };
  const auto refuse = [&](std::string_view code) { done(sync::MicResult{std::string(code), {}}); };
  const auto layout = captureLayout(config);
  if (!layout.ok())
    return refuse("no_capture");
  const auto& format = layout.value();
  auto opened = open_(config);
  if (!opened.ok())
    return refuse("no_capture");
  auto& source = *opened.value();
  // every microphone kept apart (the references left out), at the capture rate for ultrasound, else 16 khz
  const auto rate = native ? static_cast<double>(format.sampleRate) : sync::kCalibrationMicRate;
  const auto keep = sync::recordingChannels(std::min(format.microphones.size(), sync::kMaxMicChannels),
                                            toMono - fromMono + 300'000'000, rate);
  auto split = MicSplit::create(format, keep, static_cast<std::uint32_t>(rate));
  if (!split.ok())
    return refuse("no_capture");
  if (!source.open({.sampleRate = format.sampleRate,
                    .channels = format.channels,
                    .periodFrames = format.periodFrames,
                    .bufferFrames = format.bufferFrames})
           .ok())
    return refuse("no_capture");
  std::vector<std::int16_t> captured(format.periodFrames * format.channels);
  std::vector<std::int16_t> mics;
  sync::MicResult result;
  result.recording.channels = split.value().channels();
  result.recording.rate = rate;
  log_.info("calibration_record", {{"mode", "raw"},
                                   {"ms", (toMono - fromMono) / 1'000'000},
                                   {"rate", static_cast<std::int64_t>(rate)},
                                   {"channels", static_cast<std::uint64_t>(result.recording.channels)}});
  bool muted = false;
  while (!stopped.load()) {
    const auto read = source.read(captured, kReadTimeout);
    const auto now = sync::monoNs();
    if (read.kind == CaptureRead::Kind::Failed) {
      source.close();
      return refuse("no_capture");
    }
    if (read.kind != CaptureRead::Kind::Frames)
      continue;
    mics.clear();
    split.value().push(std::span(captured).first(std::min(read.frames, format.periodFrames) * format.channels), mics);
    if (now < fromMono)
      continue;
    if (muted_.load())
      muted = true;
    auto& rec = result.recording;
    rec.pcm.insert(rec.pcm.end(), mics.begin(), mics.end());
    rec.stamps.push_back({rec.frames(), now});
    if (now >= toMono)
      break;
  }
  source.close();
  // nothing heard while muted leaves the device (rule 8): the recording goes unread
  if (muted)
    return refuse("muted");
  if (stopped.load() && sync::monoNs() < toMono)
    return refuse("no_capture");
  done(std::move(result));
}

void Capture::run(std::uint64_t generation, const CaptureConfig& config, const std::atomic<bool>& stopped) {
  // the reasons cadenced gets; the log carries the stage and detail token too
  const auto report = [&](std::string_view reason) {
    loop_.post([this, generation, reason] { fail(generation, reason); });
  };
  const bool file = config.backend == cadence_audiod_v1_Backend_BACKEND_FILE;
  const auto layout = captureLayout(config);
  if (!layout.ok()) {
    log_.warn("capture_failed", {{"stage", "layout"}});
    report("format");
    return;
  }
  const auto& format = layout.value();
  auto opened = open_(config);
  if (!opened.ok()) {
    log_.warn("capture_failed", {{"stage", "source"}, {"reason", opened.error()}});
    report(opened.error() == "format" ? "format" : "open_failed");
    return;
  }
  auto& source = *opened.value();
  OpusEncoder encoder;
  if (!encoder.open().ok()) {
    log_.warn("capture_failed", {{"stage", "encoder"}});
    report("encode_failed");
    return;
  }
  auto downmix = MicDownmix::create(format);
  if (!downmix.ok()) {
    log_.warn("capture_failed", {{"stage", "layout"}});
    report("format");
    return;
  }
  const auto granted = source.open({.sampleRate = format.sampleRate,
                                    .channels = format.channels,
                                    .periodFrames = format.periodFrames,
                                    .bufferFrames = format.bufferFrames});
  if (!granted.ok()) {
    log_.warn("capture_failed", {{"stage", "open"}, {"reason", token(granted.error().code)}});
    report(granted.error().code == CaptureErrorCode::FormatMismatch ? "format" : "open_failed");
    return;
  }
  std::vector<std::int16_t> captured(format.periodFrames * format.channels);
  // 16 kHz mono not yet in a packet
  std::vector<std::int16_t> mono;
  std::vector<std::int16_t> frame(kEncoderFrameSamples);
  std::uint64_t recovered = 0;
  std::uint64_t encodeErrors = 0;
  std::uint64_t encodeStreak = 0;

  // the first filled samples of frame, zero padded; false once the encoder keeps failing: one bad frame is skipped,
  // a run of them ends the capture
  const auto emit = [&](std::size_t filled) {
    std::fill(frame.begin() + static_cast<std::ptrdiff_t>(filled), frame.end(), std::int16_t{0});
    auto packet = encoder.encode(frame);
    if (!packet.ok()) {
      if (sparse(++encodeErrors))
        log_.warn("encode_failed", {{"count", encodeErrors}});
      return ++encodeStreak < kEncodeFailureLimit;
    }
    encodeStreak = 0;
    loop_.post([this, generation, opus = std::move(packet.value())] { send(generation, opus); });
    return true;
  };
  // the oldest held samples, up to a frame
  const auto take = [&] {
    const auto count = std::min(mono.size(), frame.size());
    std::copy_n(mono.begin(), count, frame.begin());
    mono.erase(mono.begin(), mono.begin() + static_cast<std::ptrdiff_t>(count));
    return count;
  };
  const auto failEncoding = [&] {
    source.close();
    report("encode_failed");
  };

  while (!stopped.load()) {
    const auto read = source.read(captured, kReadTimeout);
    switch (read.kind) {
      case CaptureRead::Kind::Frames:
        downmix.value().push(std::span(captured).first(std::min(read.frames, format.periodFrames) * format.channels),
                             mono);
        // read on while muted, so the device never overruns, but let nothing out
        if (muted_.load()) {
          mono.clear();
          break;
        }
        while (mono.size() >= frame.size()) {
          if (!emit(take()))
            return failEncoding();
        }
        break;
      case CaptureRead::Kind::Recovered:
        if (sparse(++recovered))
          log_.warn("capture_recovered", {{"count", recovered}});
        break;
      case CaptureRead::Kind::Idle:
        // a file's last frame may be short; a live device never idles with audio held back
        if (file && !mono.empty() && !emit(take()))
          return failEncoding();
        break;
      case CaptureRead::Kind::Failed:
        log_.warn("capture_failed", {{"stage", "read"}, {"reason", read.error ? token(read.error->code) : "device"}});
        source.close();
        report("read_failed");
        return;
    }
  }
  source.close();
}

}  // namespace cadence::audiod

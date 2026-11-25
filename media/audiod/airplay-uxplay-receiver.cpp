#include "audiod/airplay-uxplay-receiver.h"

extern "C" {
#include "cadence_dnssd.h"
#include "logger.h"
#include "raop.h"
}

#include <atomic>
#include <cstdio>
#include <cstring>
#include <string>
#include <utility>

namespace cadence::audiod::uxplay {

namespace {

// airplay's feature bits (the advertised "features"/"ft")
constexpr int kFeatureVideo = 0;
constexpr int kFeatureHls = 4;
constexpr int kFeatureScreen = 7;
constexpr int kFeatureScreenMultiCodec = 42;
// dnssd_init's pin_pw: a pin shown on screen (pair-pin-start)
constexpr unsigned char kPinOnScreen = 1;

std::atomic<const cadence_dnssd_backend_t*> backend_{nullptr};

// linux: the system's avahi-daemon, unless the embedder set a backend first. made once and kept for the process: it
// reconnects to the daemon on its own
const cadence_dnssd_backend_t* defaultBackend(const Log& log) {
#ifdef CADENCE_AIRPLAY_DNSSD_AVAHI
  static const cadence_dnssd_backend_t* avahi = [&log]() -> const cadence_dnssd_backend_t* {
    int error = 0;
    auto* made = cadence_dnssd_avahi_new(&error);
    if (made == nullptr)
      log.warn("airplay_avahi_failed", {{"code", error}});
    return made;
  }();
  return avahi;
#else
  (void)log;
  return nullptr;
#endif
}

std::string_view text(const char* value) { return value != nullptr ? std::string_view(value) : std::string_view(); }

}  // namespace

void setDnssdBackend(const cadence_dnssd_backend_t* backend) { backend_.store(backend, std::memory_order_release); }

const cadence_dnssd_backend_t* dnssdBackend() { return backend_.load(std::memory_order_acquire); }

struct Receiver::Impl {
  const Log& log;
  Events& events;
  raop_callbacks_t callbacks{};
  dnssd_t* dnssd = nullptr;
  raop_t* raop = nullptr;
  bool started = false;

  Impl(const Log& logRef, Events& eventsRef) : log(logRef), events(eventsRef) {}

  ~Impl() {
    if (dnssd != nullptr) {
      dnssd_unregister_airplay(dnssd);
      dnssd_unregister_raop(dnssd);
    }
    // stops the httpd, which ends every connection and joins the threads that call back
    if (raop != nullptr) {
      if (started)
        raop_stop_httpd(raop);
      raop_destroy(raop);
    }
    if (dnssd != nullptr)
      dnssd_destroy(dnssd);
  }

  static Events& of(void* cls) { return static_cast<Impl*>(cls)->events; }
};

namespace {

using Impl = Receiver::Impl;

// libairplay's lines can carry a pin, a sender's name or its keys: none of their text is passed on (rule 8), only that
// an error happened
void logLine(void* cls, int level, const char* /*msg*/) {
  if (level <= LOGGER_ERR)
    static_cast<Impl*>(cls)->log.warn("airplay_lib_error", {{"level", level}});
}

void audioProcess(void* cls, raop_ntp_t* /*ntp*/, audio_decode_struct* data) {
  if (data == nullptr || data->data == nullptr || data->data_len <= 0)
    return;
  Impl::of(cls).audio(AudioPacket{
      .data = {data->data, static_cast<std::size_t>(data->data_len)},
      .codec = codecOf(data->ct),
      .localNs = static_cast<std::int64_t>(data->ntp_time_local),
      .remoteNs = static_cast<std::int64_t>(data->ntp_time_remote),
      .rtpTime = data->rtp_time,
      .seq = data->seqnum,
  });
}

void videoProcess(void* cls, raop_ntp_t* /*ntp*/, video_decode_struct* data) {
  if (data == nullptr || data->data == nullptr || data->data_len <= 0)
    return;
  Impl::of(cls).video(VideoPacket{
      .data = {data->data, static_cast<std::size_t>(data->data_len)},
      .h265 = data->is_h265,
      .nalCount = data->nal_count,
      .localNs = static_cast<std::int64_t>(data->ntp_time_local),
      .remoteNs = static_cast<std::int64_t>(data->ntp_time_remote),
  });
}

void videoPause(void* /*cls*/) {}
void videoResume(void* /*cls*/) {}
void connFeedback(void* /*cls*/) {}
void connReset(void* cls, int reason) { Impl::of(cls).connectionReset(reason); }

void videoReset(void* cls, reset_type_t type) {
  // an hls cast's end and a nohold takeover end what the screen shows; the rest are the mirror stream's own
  if (type == RESET_TYPE_HLS_SHUTDOWN || type == RESET_TYPE_HLS_EOS || type == RESET_TYPE_HLS_CONN_CLOSED)
    Impl::of(cls).hlsStop();
}

void connInit(void* cls) { Impl::of(cls).connectionOpened(); }
void connDestroy(void* cls) { Impl::of(cls).connectionClosed(); }
void audioFlush(void* cls) { Impl::of(cls).audioFlush(); }
void videoFlush(void* cls) { Impl::of(cls).videoFlush(); }
double audioSetClientVolume(void* cls) { return Impl::of(cls).clientVolume(); }
void audioSetVolume(void* cls, float volume) { Impl::of(cls).volume(volume); }

void audioSetMetadata(void* cls, const void* buffer, int length) {
  if (buffer != nullptr && length > 0)
    Impl::of(cls).metadata({static_cast<const std::uint8_t*>(buffer), static_cast<std::size_t>(length)});
}

void audioSetCoverart(void* cls, const void* buffer, int length) {
  if (buffer != nullptr && length > 0)
    Impl::of(cls).cover({static_cast<const std::uint8_t*>(buffer), static_cast<std::size_t>(length)});
}

void audioStopCoverart(void* /*cls*/) {}
// dacp (the sender's remote control) needs an mdns browse of the sender, which cadence's dnssd does not do
void audioRemoteControlId(void* /*cls*/, const char* /*dacpId*/, const char* /*activeRemote*/) {}
void exportDacp(void* /*cls*/, const char* /*activeRemote*/, const char* /*dacpId*/) {}

void audioSetProgress(void* cls, uint32_t* start, uint32_t* current, uint32_t* end) {
  if (start != nullptr && current != nullptr && end != nullptr)
    Impl::of(cls).progress(*start, *current, *end);
}

void audioGetFormat(void* cls, unsigned char* ct, unsigned short* spf, bool* usingScreen, bool* isMedia,
                    uint64_t* /*audioFormat*/) {
  Impl::of(cls).audioFormat(codecOf(ct != nullptr ? *ct : 0), spf != nullptr ? *spf : 0,
                            usingScreen != nullptr && *usingScreen, isMedia != nullptr && *isMedia);
}

void videoReportSize(void* cls, float* /*widthSource*/, float* /*heightSource*/, float* width, float* height) {
  if (width != nullptr && height != nullptr)
    Impl::of(cls).videoSize(*width, *height);
}

void mirrorVideoRunning(void* cls, bool running) { Impl::of(cls).mirrorRunning(running); }

void reportClientRequest(void* cls, char* deviceId, char* model, char* name, bool* admit) {
  bool admitted = true;
  Impl::of(cls).client(text(deviceId), text(model), text(name), admitted);
  if (admit != nullptr)
    *admit = admitted;
}

void displayPin(void* cls, char* pin) {
  if (pin != nullptr)
    Impl::of(cls).pin(text(pin));
}

void registerClient(void* cls, const char* deviceId, const char* publicKey, const char* /*name*/) {
  Impl::of(cls).registered(text(deviceId), text(publicKey));
}

bool checkRegister(void* cls, const char* publicKey) { return Impl::of(cls).known(text(publicKey)); }

// no password: a pin, when asked for, is pair-pin-start's
const char* passwd(void* /*cls*/, int* length) {
  if (length != nullptr)
    *length = 0;
  return nullptr;
}

int videoSetCodec(void* cls, video_codec_t codec) {
  if (codec != VIDEO_CODEC_H264 && codec != VIDEO_CODEC_H265)
    return -1;
  return Impl::of(cls).videoCodec(codec == VIDEO_CODEC_H265) ? 0 : -1;
}

void onVideoPlay(void* cls, const char* location, const float start) {
  if (location != nullptr)
    Impl::of(cls).hlsPlay(text(location), start);
}

void onVideoWaitReady(void* /*cls*/) {}
void onVideoScrub(void* cls, const float position) { Impl::of(cls).hlsScrub(position); }
void onVideoRate(void* cls, const float rate) { Impl::of(cls).hlsRate(rate); }
void onVideoStop(void* cls) { Impl::of(cls).hlsStop(); }

void onVideoAcquirePlaybackInfo(void* cls, playback_info_t* info) {
  if (info == nullptr)
    return;
  const auto now = Impl::of(cls).hlsInfo();
  info->duration = now.durationS;
  info->position = now.positionS;
  info->rate = now.rate;
  info->ready_to_play = now.ready;
  info->playback_buffer_empty = now.bufferEmpty;
  info->playback_buffer_full = now.likelyToKeepUp;
  info->playback_likely_to_keep_up = now.likelyToKeepUp;
}

float onVideoPlaylistRemove(void* /*cls*/) { return 0.0f; }

void fillCallbacks(raop_callbacks_t& c, Impl* impl) {
  std::memset(&c, 0, sizeof c);
  c.cls = impl;
  c.audio_process = audioProcess;
  c.video_process = videoProcess;
  c.video_pause = videoPause;
  c.video_resume = videoResume;
  c.conn_feedback = connFeedback;
  c.conn_reset = connReset;
  c.video_reset = videoReset;
  c.conn_init = connInit;
  c.conn_destroy = connDestroy;
  c.audio_flush = audioFlush;
  c.video_flush = videoFlush;
  c.audio_set_client_volume = audioSetClientVolume;
  c.audio_set_volume = audioSetVolume;
  c.audio_set_metadata = audioSetMetadata;
  c.audio_set_coverart = audioSetCoverart;
  c.audio_stop_coverart_rendering = audioStopCoverart;
  c.audio_remote_control_id = audioRemoteControlId;
  c.audio_set_progress = audioSetProgress;
  c.audio_get_format = audioGetFormat;
  c.video_report_size = videoReportSize;
  c.mirror_video_running = mirrorVideoRunning;
  c.report_client_request = reportClientRequest;
  c.display_pin = displayPin;
  c.register_client = registerClient;
  c.check_register = checkRegister;
  c.passwd = passwd;
  c.export_dacp = exportDacp;
  c.video_set_codec = videoSetCodec;
  c.on_video_play = onVideoPlay;
  c.on_video_wait_ready = onVideoWaitReady;
  c.on_video_scrub = onVideoScrub;
  c.on_video_rate = onVideoRate;
  c.on_video_stop = onVideoStop;
  c.on_video_acquire_playback_info = onVideoAcquirePlaybackInfo;
  c.on_video_playlist_remove = onVideoPlaylistRemove;
}

std::string hwString(const std::array<std::uint8_t, 6>& hw) {
  char out[18];
  std::snprintf(out, sizeof out, "%02X:%02X:%02X:%02X:%02X:%02X", hw[0], hw[1], hw[2], hw[3], hw[4], hw[5]);
  return out;
}

}  // namespace

std::unique_ptr<Receiver> Receiver::start(const Log& log, const ReceiverSettings& settings, Events& events) {
  const auto fail = [&](std::string_view call, int code) -> std::unique_ptr<Receiver> {
    log.warn("airplay_start_failed", {{"call", call}, {"code", code}});
    return nullptr;
  };
  if (settings.name.empty() || settings.name.size() > 200)
    return fail("name", 0);
  auto impl = std::make_unique<Impl>(log, events);
  int error = 0;
  impl->dnssd = dnssd_init(settings.name.data(), static_cast<int>(settings.name.size()),
                           reinterpret_cast<const char*>(settings.hwAddress.data()),
                           static_cast<int>(settings.hwAddress.size()), &error,
                           settings.requirePin ? kPinOnScreen : 0);
  if (impl->dnssd == nullptr)
    return fail("dnssd_init", error);
  dnssd_set_airplay_features(impl->dnssd, kFeatureScreen, settings.mirroring ? 1 : 0);
  dnssd_set_airplay_features(impl->dnssd, kFeatureScreenMultiCodec, settings.mirroring && settings.h265 ? 1 : 0);
  dnssd_set_airplay_features(impl->dnssd, kFeatureVideo, settings.hls ? 1 : 0);
  dnssd_set_airplay_features(impl->dnssd, kFeatureHls, settings.hls ? 1 : 0);
  const auto* backend = dnssdBackend();
  if (backend == nullptr)
    backend = defaultBackend(log);
  if (backend != nullptr)
    cadence_dnssd_set_backend(impl->dnssd, backend);

  fillCallbacks(impl->callbacks, impl.get());
  impl->raop = raop_init(&impl->callbacks);
  if (impl->raop == nullptr)
    return fail("raop_init", 0);
  raop_set_log_callback(impl->raop, logLine, impl.get());
  raop_set_log_level(impl->raop, LOGGER_ERR);
  const auto device = hwString(settings.hwAddress);
  // nohold: the newest sender takes over, as the last to play takes the session
  if (const int rc = raop_init2(impl->raop, 1, device.c_str(), settings.keyFile.c_str()); rc != 0)
    return fail("raop_init2", rc);
  raop_set_plist(impl->raop, "width", static_cast<int>(settings.maxWidth != 0 ? settings.maxWidth : 1920));
  raop_set_plist(impl->raop, "height", static_cast<int>(settings.maxHeight != 0 ? settings.maxHeight : 1080));
  raop_set_plist(impl->raop, "refreshRate", static_cast<int>(settings.maxFps));
  raop_set_plist(impl->raop, "maxFPS", static_cast<int>(settings.maxFps));
  raop_set_plist(impl->raop, "hls", settings.hls ? 1 : 0);
  // 0: a random pin for each pair-pin-start
  if (settings.requirePin)
    raop_set_plist(impl->raop, "pin", 0);

  unsigned short port = settings.port;
  raop_set_port(impl->raop, port);
  if (const int rc = raop_start_httpd(impl->raop, &port); rc < 0)
    return fail("raop_start_httpd", rc);
  impl->started = true;
  raop_set_dnssd(impl->raop, impl->dnssd);
  if (const int rc = dnssd_register_raop(impl->dnssd, port); rc != 0)
    return fail("dnssd_register_raop", rc);
  if (const int rc = dnssd_register_airplay(impl->dnssd, port); rc != 0)
    return fail("dnssd_register_airplay", rc);
  log.info("airplay_receiver_started", {{"port", static_cast<unsigned>(port)},
                                        {"mirroring", settings.mirroring ? "on" : "off"},
                                        {"h265", settings.h265 ? "on" : "off"},
                                        {"pin", settings.requirePin ? "on" : "off"},
                                        {"advertised", backend != nullptr ? "yes" : "no"}});
  return std::unique_ptr<Receiver>(new Receiver(std::move(impl), port));
}

Receiver::Receiver(std::unique_ptr<Impl> impl, std::uint16_t port) : impl_(std::move(impl)), port_(port) {}

Receiver::~Receiver() = default;

void Receiver::disconnect() { raop_remove_known_connections(impl_->raop); }

std::string Receiver::raopTxt() const {
  int length = 0;
  const char* txt = dnssd_get_raop_txt(impl_->dnssd, &length);
  return {txt, static_cast<std::size_t>(length > 0 ? length : 0)};
}

std::string Receiver::airplayTxt() const {
  int length = 0;
  const char* txt = dnssd_get_airplay_txt(impl_->dnssd, &length);
  return {txt, static_cast<std::size_t>(length > 0 ? length : 0)};
}

}  // namespace cadence::audiod::uxplay

extern "C" void cadence_audiod_airplay_dnssd(const cadence_dnssd_backend_t* backend) {
  cadence::audiod::uxplay::setDnssdBackend(backend);
}

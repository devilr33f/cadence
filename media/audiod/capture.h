#pragma once

#include "audiod/capture-engine.h"
#include "audiod/daemon.h"
#include "audiod/ipc.h"
#include "audiod/listen-capture.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/traces.h"
#include "audiod/vendor-probe.h"
#include "capture/alsa-capture.h"
#include "result.h"
#include "sync/calibration-run.h"

#ifdef CADENCE_MEDIA_KWS
#include "audiod/keyword-capture.h"
#endif

#include <atomic>
#include <chrono>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::audiod {

using CaptureConfig = cadence_audiod_v1_CaptureConfig;

// the error is a reason token, safe to log
using CaptureFactory = std::function<Result<std::unique_ptr<PcmSource>, std::string_view>(const CaptureConfig& config)>;

// alsa: the configured device, in the format the board's layout gives it. file: the wav at file_path, played from
// its start at real-time pace, which needs 16 kHz mono
Result<std::unique_ptr<PcmSource>, std::string_view> openCapture(const CaptureConfig& config);

// the format a capture opens its source in: the board's layout (boardLayout) for alsa, 16 kHz mono in 20 ms periods
// for the file backend
Result<CaptureLayout, std::string_view> captureLayout(const CaptureConfig& config);

// the device's microphone. each 20 ms Opus packet (24 kbps voip, 16 kHz mono) of a session goes to cadenced as
// MicOpus{session_id, seq from 0} through Loop::post, so no capture thread waits for the loop or the socket.
// CaptureStop, a newer CaptureStart, a new Configure and cadenced going away end the session.
//
// how the microphone is read follows the Configure:
// - alsa with vendor engines enabled: the files are checked and the engines probed in a child (vendor-probe.h), then
//   the device stays open under the board's vqe and spotter (capture-engine.h) until the next Configure. a session's
//   packets carry the vqe's output, starting with up to 300 ms heard since the last wake when it opens within
//   VendorOptions::wakePreroll of it. the spotter's wake and command hits go out as WakeEvent{engine "stock"} and
//   CommandWord{id: the index in the engines' command_words} whether or not a session is open; a phrase none of them
//   matches is dropped and counted (command_unknown). a session started while the probe runs waits for its verdict.
//   engines that take longer than VendorOptions::startLimit to start once their probes passed make audiod exit with
//   kExitEnginesHung (vendor-probe.h), so cadenced restarts it and counts the crash
// - alsa with a keyword section (the board's own keyword model, keyword-capture.h): the model file is checked
//   against its allowlisted sha256 and loaded (keyword-model.h), then the device stays open under the spotter until
//   the next Configure. a session streams the same capture, the layout's microphones mixed to mono at 16 kHz,
//   starting with up to 300 ms heard after the keyword when it opens within VendorOptions::wakePreroll of the wake.
//   a detection goes out as WakeEvent{engine "vk-kws", score} while no session is open; one during a session is held
//   (counted, as the stock stops listening for a turn), and the session's end re-arms the spotter. a model that keeps
//   failing stops the engine: VendorStatus{false, invoke_failed}, raw capture from then on, an open session going on
//   raw. an audiod built without the keyword engine answers the section with keyword_unsupported and raw capture.
//   the vendor engines are not read then
// - alsa without engines, or with engines the files, the probe or their start refused: raw capture, opened for each
//   session, the layout's microphones mixed to mono at 16 kHz; wake is the button's alone
// - file (the sim): raw capture of the wav, whatever the engines say; with a keyword section the keyword engine runs
//   on the wav as on a device, from the Configure on (an on-device check of the model, no microphone needed)
// an alsa Configure, or a keyword one, is answered after its Ready by VendorStatus{engines, code}: code is empty
// when everything runs, else disabled, layout_invalid, library_missing, model_missing, probe_failed, probe_timeout or
// start_failed with the engines off, or command_model_missing, command_model_failed or spotter_failed with them on
// (spotter_failed: the vqe serves button sessions, and nothing wakes). the keyword engine answers keyword when it
// runs, else kind_unknown, model_unknown, model_missing, model_invalid, model_shape, arena_exceeded, layout_invalid or
// start_failed with it off.
//
// WakeListen{enabled} (the custom wake word, cadenced's detector) has the microphone stream to cadenced as MicPcm while
// no session is open: the vendor engines' vqe output, the keyword engine's mono capture, or, in raw capture, a
// ListenCapture audiod keeps open for it (mode Listen: sessions then stream from it, as the keyword engine's do, and a
// raw session already running finishes first). a CaptureStart's preroll_from_ms starts the session with the frames
// kept from that instant on. nothing goes out while muted; the listening ends with the connection (reset).
//
// MuteState{muted} (the mute switch) keeps the device open but lets no packet, wake or command out while muted; the
// engines then hear silence (the keyword spotter hears nothing), and an unmute starts their warm-up again. a session
// still open streams on after it.
//
// a raw packet that cannot go out (the client stopped reading) is dropped and counted, and its seq is skipped, so
// cadenced sees the gap. a capture that cannot open, read or encode sends CaptureFailed{session_id, reason}
// (open_failed, read_failed, encode_failed, format) for the current session only and stays silent until a new start.
// a start refused before anything opens (unconfigured; session_id: no safe stream id) is answered the same way,
// unless its id is no printable ascii, which is only logged. the file source has no start hint: every CaptureStart
// plays Configure's file_path. the Configure mic section is not read. every call but the capture threads' runs on
// the loop thread; ending a raw session joins its thread, which sees the stop within one 20 ms read
class Capture final : public Part {
 public:
  Capture(Loop& loop, IpcServer& ipc, const Log& log, CaptureFactory open = openCapture, VendorOptions vendor = {});
  ~Capture() override;

  // a session's trace marks (traces.h), by its id: CAPTURE_STARTED, FIRST_OPUS_FRAME (from the start to its first
  // MicOpus, as an interval; preroll), at its end CAPTURE_STATS (the session as an interval: frames, bytes, dropped)
  // and, with the engines, VQE_STATS (chunks, gaps, vqe_errors), then CAPTURE_STOPPED; and WAKE_DETECTED (score) at
  // the instant the engines' WakeEvent carries. none until set; set before the loop runs, with traces outliving this
  // part. the CaptureStart's trace id is logged beside capture_started and capture_stopped
  void traceTo(Traces& traces) { traces_ = &traces; }

  void configure(const Configure& configuration) override;
  void ready() override;
  void reset() override;

  // a group calibration's recording: the microphone over [fromMono, toMono) (CLOCK_MONOTONIC), kept in
  // memory for the calibration's analysis alone (rule 8) and handed to done once, on any thread. with the engines on
  // it is up to the first four microphones before the vqe, resampled to 16 kHz (the vqe's echo canceller takes out
  // the device's own chirp); raw capture uses the same 16 kHz microphone split on a capture thread of its own for the
  // window. refused (MicResult.code): no_capture (no capture configured, one still resolving, the keyword engine
  // holding the device, or a recording already running), capturing (a raw session holds the device), muted (now or
  // during the window). any thread
  void record(std::int64_t fromMono, std::int64_t toMono, std::function<void(sync::MicResult)> done);
  // as record, but keeps the microphones at their capture rate (48 khz on the speakers) for ultrasound. the voice
  // uplink and vqe path are untouched; a 16 khz recorder still returns 16 khz and the detector reports unsupported
  void recordNative(std::int64_t fromMono, std::int64_t toMono, std::function<void(sync::MicResult)> done);
  // end the current native recording early and drop its samples (a voice session or a mute)
  void cancelNative();

 private:
  enum class Mode : std::uint8_t { Off, Raw, Resolving, Engines, Keyword, Listen };

  struct Running {
    std::string id;
    std::shared_ptr<std::atomic<bool>> stop;
    std::thread thread;
  };

  struct Status {
    bool engines = false;
    std::string_view code;
  };

  void start(const cadence_audiod_v1_CaptureStart& request);
  void stop(const cadence_audiod_v1_CaptureStop& request);
  void mute(const cadence_audiod_v1_MuteState& request);
  void listen(const cadence_audiod_v1_WakeListen& request);
  // raw capture opens or closes its ListenCapture as listening and the sessions allow
  void settleListen();
  void startListen();
  void stopListen();
  // the custom wake word's frames, from any capture's thread, sent from the loop while they may go
  [[nodiscard]] std::function<void(std::uint64_t, std::int64_t, std::vector<std::int16_t>, bool)> pcmSink(
      std::uint64_t epoch);
  void sendPcm(std::uint64_t seq, std::int64_t atMs, const std::vector<std::int16_t>& pcm, bool gap);
  // a start that passed its checks, in the current mode
  void begin(std::string_view id);
  void beginPending();
  void end();
  // a session's packets from the capture that stays open (the engines' or the keyword engine's)
  void openStream(std::uint64_t generation, bool preroll);
  void closeStream();
  // a raw capture thread for the session of generation
  void runRaw(std::uint64_t generation);
  // everything the configuration opened, gone
  void teardown();

  void resolved(VendorVerdict verdict);
  void startEngines(VendorSetup setup, std::string_view code);
  void enginesStarted(std::uint64_t epoch, const Result<AudioEngineInfo, AudioEngineError>& started,
                      std::string_view code, bool commands);
  // the start ran past VendorOptions::startLimit
  void enginesHung();
  void disarmStartWatch();
  void stopEngines();
  // raw capture from here on, and the reason why
  void fallBack(std::string_view code);
  [[nodiscard]] EngineEvents events(std::uint64_t epoch);
  // the session packets and encoder failures a capture's threads post, the engines' and the keyword engine's alike
  [[nodiscard]] std::function<void(std::uint64_t, std::vector<std::uint8_t>)> packetSink();
  [[nodiscard]] std::function<void(std::uint64_t)> failureSink();
  void unknownCommand();
#ifdef CADENCE_MEDIA_KWS
  // capture-keyword.cpp
  void startKeyword();
  [[nodiscard]] KeywordEvents keywordEvents(std::uint64_t epoch);
  void keywordWake(float score, std::int64_t atMs, bool inSession);
  // the model kept failing: raw capture from now on
  void keywordStopped(std::string_view code);
  void stopKeyword();
#endif
  // after Ready: held until it went out
  void report(Status status);
  void sendStatus(Status status);
  // atUnixNs: when the engine heard it, CLOCK_REALTIME
  void sendWake(std::string_view engine, float score, std::int64_t atUnixNs);
  void mark(trace::Stage stage, std::initializer_list<trace::Value> values = {}, std::int64_t atUnixNs = 0,
            std::int64_t durationNs = 0);
  void sendCommand(std::uint32_t id, std::int64_t atMs);

  void send(std::uint64_t generation, const std::vector<std::uint8_t>& opus);
  // a start that opens nothing: logged, and answered when cadenced can read its id back
  void refuse(std::string_view id, std::string_view reason);
  void fail(std::uint64_t generation, std::string_view reason);
  void sendFailed(std::string_view id, std::string_view reason);
  // the loop's side of record
  void startRecording(std::int64_t fromMono, std::int64_t toMono, std::function<void(sync::MicResult)> done,
                      bool native = false);
  void finishRecording();
  // a raw recording's thread: reads from the window's start to its end, then hands the recording over
  void recordRaw(const CaptureConfig& config, std::int64_t fromMono, std::int64_t toMono,
                 std::function<void(sync::MicResult)> handOver, const std::atomic<bool>& stopped, bool native);

  // the raw capture thread: a failure tells the loop and ends the thread
  void run(std::uint64_t generation, const CaptureConfig& config, const std::atomic<bool>& stopped);

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  CaptureFactory open_;
  VendorOptions vendor_;
  VendorResolver resolver_;
  std::optional<CaptureConfig> config_;
  Mode mode_ = Mode::Off;
  // counts configurations, so what an earlier one's engines posted is told apart
  std::uint64_t epoch_ = 0;
  bool readied_ = false;
  std::optional<Status> held_;
  // the engines and the thread that starts them, whose result comes back through the loop
  std::unique_ptr<CaptureEngine> engine_;
  std::thread starting_;
  // armed while starting_ runs: the start's limit
  std::optional<Loop::TimerId> startWatch_;
  // a session started while the engines were being resolved
  std::string pending_;
  // when the last WakeEvent went out, for the pre-roll of the session that follows it
  std::optional<std::chrono::steady_clock::time_point> lastWake_;
  // command phrases none of the configured words matched, while these engines ran
  std::uint64_t unknownCommands_ = 0;
#ifdef CADENCE_MEDIA_KWS
  // the keyword engine, while it runs, and the detections a session or the mute switch held back
  std::unique_ptr<KeywordCapture> keyword_;
  std::uint64_t heldWakes_ = 0;
#endif
  bool streaming_ = false;
  std::optional<Running> running_;
  // the custom wake word listens (WakeListen), the raw capture held open for it, and the current session's pre-roll
  // instant (CaptureStart.preroll_from_ms)
  bool listen_ = false;
  std::unique_ptr<ListenCapture> listening_;
  std::int64_t prerollFrom_ = 0;
  std::uint64_t pcmDropped_ = 0;
  // the mute switch, which the raw capture thread reads too
  std::atomic<bool> muted_{false};
  // counts sessions, so a packet an ended one posted is told apart
  std::uint64_t generation_ = 0;
  std::string sessionId_;
  std::uint32_t seq_ = 0;
  std::uint64_t dropped_ = 0;
  // the session's trace: its CaptureStart's trace id, when it began (unix ns), what went out, and the engines' counts
  // at its start
  Traces* traces_ = nullptr;
  std::string traceId_;
  std::int64_t startedNs_ = 0;
  std::uint64_t bytes_ = 0;
  bool firstSent_ = false;
  bool preroll_ = false;
  AudioEngineStatus enginesAtStart_;

  // a calibration's recording: the engines' tap (its samples gathered under the mutex, handed over by a timer at the
  // window's end) or a raw thread of its own
  struct Tapped {
    std::mutex mutex;
    sync::MicRecording recording;
    // the current tap chunk split to microphones at 16 kHz, reused under the same mutex
    std::vector<std::int16_t> converted;
    std::int64_t fromMono = 0;
    std::int64_t toMono = 0;
    bool muted = false;
  };
  std::shared_ptr<Tapped> tapped_;
  std::function<void(sync::MicResult)> tappedDone_;
  std::optional<Loop::TimerId> tapTimer_;
  std::optional<Running> recorder_;
  bool nativeRecording_ = false;
};

}  // namespace cadence::audiod

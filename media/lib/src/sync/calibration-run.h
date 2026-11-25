#pragma once

#include "sync/calibration.h"
#include "sync/group-playout.h"

#include "cadence/sync/v1/sync.pb.h"

#include <algorithm>
#include <cstdint>
#include <functional>
#include <map>
#include <optional>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

// a calibration as the group runs it: the leader sends CalibrateStart, every member (the leader too) places its
// chirps (one per round) in its playout and records its microphones over the window, analyses the recording off its
// threads and answers CalibrateArrivals; the leader solves them (calibration.h). the recording lives in memory for
// the analysis alone (rule 8)
namespace cadence::sync {

inline constexpr double kCalibrationMicRate = 16000;
// the coded-probe wire and analysis version; a member advertises it in Join
inline constexpr std::uint32_t kCalibrationVersion = 2;
// ultrasonic capability rounds and live tracking envelopes; advertised through Status.calibration_version too
inline constexpr std::uint32_t kTrackingVersion = 3;
// the first slot is this far after the start goes out: past what a member needs to take it and place its chirp
inline constexpr std::int64_t kCalibrationLeadNs = 1'000'000'000;
// how long past the window's end the leader waits for every member's arrivals
inline constexpr std::int64_t kCalibrationAnswerNs = 3'000'000'000;
// a member that acknowledged its recording asks for this long past the window; the leader never grants past 30 s
inline constexpr std::uint32_t kCalibrationAnalysisBudgetMs = 20'000;
inline constexpr std::uint32_t kCalibrationMaxAnalysisBudgetMs = 30'000;
// silence placed around a chirp, so its fades and the playout's run-out fall outside it (past a chirp, at least
// this, and 100 ms more than the chirp lasts)
inline constexpr std::int64_t kChirpBeforeNs = 100'000'000;
inline constexpr std::int64_t kChirpAfterNs = 400'000'000;
// a listener calibration (capschema's GroupListenerStartMs, GroupListenerSlotMs): the leader's chirp this long after
// the request, every next slot this far after the last, and the group busy this long past the last one's end
inline constexpr std::int64_t kListenerStartNs = 1'500'000'000;
inline constexpr std::int64_t kListenerSlotNs = 1'200'000'000;
inline constexpr std::int64_t kListenerTailNs = 500'000'000;
// a recording longer than this is refused (a listener calibration of eight members, four rounds, is about 38 s)
inline constexpr std::int64_t kMaxRecordingNs = 60'000'000'000;
// what a recording may hold in memory: its channels are capped to fit
inline constexpr std::size_t kMaxRecordingBytes = 8u << 20;
inline constexpr std::size_t kMaxMicChannels = 8;

// how many microphone channels a recording of this long at rate may keep, of the channels it has
[[nodiscard]] std::size_t recordingChannels(std::size_t channels, std::int64_t durationNs, double rate);

struct MicRecording {
  // interleaved: channels microphones, at rate
  std::vector<std::int16_t> pcm;
  std::size_t channels = 1;
  double rate = kCalibrationMicRate;
  // when each read returned (local CLOCK_MONOTONIC), for the recording's clock: endFrame counts frames, not samples
  std::vector<calibration::ReadStamp> stamps;

  [[nodiscard]] std::size_t frames() const { return channels == 0 ? 0 : pcm.size() / channels; }
};

// a recording's outcome: code empty with the recording, else a token (no_capture, muted)
struct MicResult {
  std::string code;
  MicRecording recording;
};

// what one member's analysis did, for a numbers-and-codes-only log line (rule 8)
struct CalibrationAnalysis {
  std::uint32_t id = 0;
  std::int64_t ms = 0;
  std::uint64_t frames = 0;
  std::uint32_t rate = 0;
  std::uint64_t channels = 0;
  std::string code;
};

// what the device lends a calibration
struct CalibrationHooks {
  // record the microphone over [fromMono, toMono) (local CLOCK_MONOTONIC), in memory only; done runs once, on any
  // thread
  std::function<void(std::int64_t fromMono, std::int64_t toMono, std::function<void(MicResult)> done)> record;
  // native-rate capture for a run with ultrasound (48 khz on the speakers); empty: ultrasound is unsupported
  std::function<void(std::int64_t fromMono, std::int64_t toMono, std::function<void(MicResult)> done)> recordNative;
  // stop a live cycle early and drop its buffer (a voice session or a mute)
  std::function<void()> cancelNative;
  // empty when the device may play its chirp now, else a token (busy: another source holds the speaker; gated: the
  // volume does not hold yet)
  std::function<std::string()> gate;
  // sent to the leader once the recording reaches the analysis worker
  std::uint32_t analysisBudgetMs = kCalibrationAnalysisBudgetMs;
  // runs after the analysis, on its worker thread
  std::function<void(const CalibrationAnalysis&)> analysed;
};

// the plan a CalibrateStart carries: its rounds (0 is 1), its chirp (absent: the one before rounds), each slot's gain
[[nodiscard]] calibration::Plan planOf(const cadence_sync_v1_CalibrateStart& start);
// the CalibrateStart of a plan: every slot with its gain, the rounds and the chirp
[[nodiscard]] cadence_sync_v1_CalibrateStart startOf(const calibration::Plan& plan, std::uint32_t id, std::uint32_t epoch);
// a live cycle is a one-round plan whose probe is the fixed ultrasonic shape (no shape on the wire)
[[nodiscard]] calibration::Plan planOf(const cadence_sync_v1_TrackCycle& cycle);
[[nodiscard]] cadence_sync_v1_TrackCycle cycleOf(const calibration::Plan& plan, std::uint32_t id, std::uint32_t epoch);
// the two arrival envelopes carry the same coded observations
[[nodiscard]] cadence_sync_v1_TrackArrivals trackOf(const cadence_sync_v1_CalibrateArrivals& arrivals);
[[nodiscard]] cadence_sync_v1_CalibrateArrivals calibrateOf(const cadence_sync_v1_TrackArrivals& arrivals);

// member's chirps of the plan (its slots of every round, each at its gain) queued into the playout in the
// calibration's epoch, one run of frames from before its first to past its last with silence between; the playout's
// trim is taken back out, so each chirp is due at its slot itself and a calibration measures the device's own latency
// whatever its trim. false when the plan does not name member
bool placeChirps(GroupPlayout& playout, std::uint32_t epoch, const calibration::Plan& plan, const std::string& member);
// as placeChirps, but a coded plan plays each slot's own gold code through probeAt
bool placeProbes(GroupPlayout& playout, std::uint32_t epoch, const calibration::Plan& plan, const std::string& member);

// a recording analysed into the CalibrateArrivals member answers. toLeader maps the device's mono time to the leader's
[[nodiscard]] cadence_sync_v1_CalibrateArrivals arrivalsOf(std::uint32_t id, const calibration::Plan& plan,
                                                           const std::string& member, const MicResult& mic,
                                                           const std::function<double(double monoNs)>& toLeader);
// arrivalsOf, with its wall time and the recording's shape reported after it
[[nodiscard]] cadence_sync_v1_CalibrateArrivals analyseRecording(
    std::uint32_t id, const calibration::Plan& plan, const std::string& member, const MicResult& mic,
    const std::function<double(double monoNs)>& toLeader,
    const std::function<void(const CalibrationAnalysis&)>& analysed = {});

[[nodiscard]] cadence_sync_v1_CalibrateArrivals refusal(std::uint32_t id, std::string_view code);

// a calibration's result as the leader reports it (GroupCalibrated)
struct CalibrationMemberOutcome {
  std::string id;
  std::string code;
  std::int32_t offsetUs = 0;
  float confidence = 0;
  // the largest spread and the fewest rounds among the arrivals its offset rests on, and the microphone channels it
  // analysed
  std::uint32_t spreadUs = 0;
  std::uint32_t rounds = 0;
  std::uint32_t micChannels = 0;
  // the solution's two-sided 95% interval half-width; 0 for a legacy solve
  std::uint32_t ci95Us = 0;
  // the answer's time after the recording window, clamped to zero for an early refusal; -1 while none came
  std::int32_t answeredMs = -1;
  // the extra round's capability: ok, weak, missed or unsupported; empty when none ran
  std::string ultrasonic;
};

// a listener calibration's slot: id's chirp of round round starts offsetMs after the first (the leader's of round 0)
struct CalibrationSlotOutcome {
  std::string id;
  std::uint32_t offsetMs = 0;
  std::uint32_t round = 0;
  std::uint32_t code = 0;
};

struct CalibrationOutcome {
  std::string code;
  std::vector<CalibrationMemberOutcome> members;
  std::uint32_t residualUs = 0;
  // listener mode (code scheduled): the first slot starts this long after the answer
  std::uint32_t startsInMs = 0;
  std::vector<CalibrationSlotOutcome> slots;
  // listener mode (code scheduled): the run listener_result names, its rounds and the chirp played
  std::uint32_t runId = 0;
  std::uint32_t rounds = 0;
  std::optional<calibration::ChirpShape> chirp;
  std::optional<calibration::ProbeShape> probe;
};

[[nodiscard]] CalibrationOutcome refused(std::string_view code);

// the leader's side of one calibration: the plan's members' arrivals as they come, then the solution. offline: the
// group's members that were not connected, which it reports too. one thread at a time
class CalibrationCollector {
 public:
  CalibrationCollector(std::uint32_t id, calibration::Plan plan, std::vector<std::string> offline);

  // false for another calibration's or a member the plan does not have. atNs is leader sync time
  bool add(const std::string& member, const cadence_sync_v1_CalibrateArrivals& arrivals, std::int64_t atNs);
  // RECORDED grants this member its own analysis wait, capped at 30 s
  bool recorded(const std::string& member, const cadence_sync_v1_CalibrateProgress& progress);
  [[nodiscard]] bool complete() const;
  [[nodiscard]] std::uint32_t answers() const {
    return static_cast<std::uint32_t>(std::count(answered_.begin(), answered_.end(), true));
  }
  // the latest wait among the members still unanswered: the old three seconds without RECORDED, its budget with it
  [[nodiscard]] std::int64_t deadlineNs() const;
  [[nodiscard]] std::uint32_t id() const { return id_; }
  [[nodiscard]] const calibration::Plan& plan() const { return plan_; }
  // the coded observations, for the live window's robust cross-cycle combine (one-round plans)
  [[nodiscard]] const std::vector<std::vector<calibration::HeardV2>>& observations() const { return heardV2_; }
  // no answer is no_answer, or analysis_timeout once that member acknowledged its recording
  [[nodiscard]] CalibrationOutcome finish() const;

 private:
  std::uint32_t id_;
  calibration::Plan plan_;
  std::vector<std::string> offline_;
  std::vector<bool> answered_;
  std::vector<bool> recorded_;
  std::vector<std::int64_t> budgetsNs_;
  std::vector<std::int32_t> answeredMs_;
  std::vector<std::string> codes_;
  std::vector<std::uint32_t> channels_;
  std::vector<std::vector<calibration::Heard>> heard_;
  std::vector<std::vector<calibration::HeardV2>> heardV2_;
  std::vector<std::vector<calibration::HeardV2>> ultrasonic_;
  std::vector<bool> ultrasonicAnswered_;
};

// one job at a time off its caller's thread: an analysis's correlation takes tens of ms on a speaker's cpu. joined
// before the next job starts and when it goes
class CalibrationWorker {
 public:
  CalibrationWorker() = default;
  ~CalibrationWorker() { join(); }
  CalibrationWorker(const CalibrationWorker&) = delete;
  CalibrationWorker& operator=(const CalibrationWorker&) = delete;

  void run(std::function<void()> job);
  void join();

 private:
  std::mutex mutex_;
  std::thread thread_;
};

}  // namespace cadence::sync

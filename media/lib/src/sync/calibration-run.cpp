#include "sync/calibration-run.h"

#include "audio/music-codec.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstring>
#include <limits>
#include <memory>

namespace cadence::sync {

namespace {

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto n = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), n);
  out[n] = '\0';
}

template <std::size_t N>
std::string textOf(const char (&field)[N]) {
  return {field, strnlen(field, N)};
}

cadence_sync_v1_ArrivalCode wireCode(calibration::ArrivalCode code) {
  switch (code) {
    case calibration::ArrivalCode::Ok:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_OK;
    case calibration::ArrivalCode::NotCovered:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_NOT_COVERED;
    case calibration::ArrivalCode::Weak:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_WEAK;
    case calibration::ArrivalCode::Unstable:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_UNSTABLE;
  }
  return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_WEAK;
}

calibration::ArrivalCode fromWire(cadence_sync_v1_ArrivalCode code) {
  switch (code) {
    case cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_OK:
      return calibration::ArrivalCode::Ok;
    case cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_NOT_COVERED:
      return calibration::ArrivalCode::NotCovered;
    case cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_UNSTABLE:
      return calibration::ArrivalCode::Unstable;
    default:
      return calibration::ArrivalCode::Weak;
  }
}

cadence_sync_v1_ArrivalCode wireCode(calibration::ArrivalCodeV2 code) {
  switch (code) {
    case calibration::ArrivalCodeV2::Ok:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_OK;
    case calibration::ArrivalCodeV2::NotCovered:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_NOT_COVERED;
    case calibration::ArrivalCodeV2::Weak:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_WEAK;
    case calibration::ArrivalCodeV2::Unstable:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_UNSTABLE;
    case calibration::ArrivalCodeV2::Missed:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_MISSED;
    case calibration::ArrivalCodeV2::Unsupported:
      return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_UNSUPPORTED;
  }
  return cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_MISSED;
}

calibration::ArrivalCodeV2 fromWireV2(cadence_sync_v1_ArrivalCode code) {
  switch (code) {
    case cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_OK:
      return calibration::ArrivalCodeV2::Ok;
    case cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_NOT_COVERED:
      return calibration::ArrivalCodeV2::NotCovered;
    case cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_UNSTABLE:
      return calibration::ArrivalCodeV2::Unstable;
    case cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_MISSED:
      return calibration::ArrivalCodeV2::Missed;
    case cadence_sync_v1_ArrivalCode_ARRIVAL_CODE_UNSUPPORTED:
      return calibration::ArrivalCodeV2::Unsupported;
    default:
      return calibration::ArrivalCodeV2::Weak;
  }
}

std::uint32_t unsignedNs(double value) {
  return static_cast<std::uint32_t>(std::clamp(std::llround(value), 0LL, 4'000'000'000LL));
}

}  // namespace

std::size_t recordingChannels(std::size_t channels, std::int64_t durationNs, double rate) {
  const double perChannel = std::max(1.0, static_cast<double>(durationNs) / 1e9 * rate) * sizeof(std::int16_t);
  const auto fit = static_cast<std::size_t>(static_cast<double>(kMaxRecordingBytes) / perChannel);
  return std::clamp<std::size_t>(std::min({channels, kMaxMicChannels, fit}), std::min<std::size_t>(channels, 1),
                                 kMaxMicChannels);
}

calibration::Plan planOf(const cadence_sync_v1_CalibrateStart& start) {
  calibration::Plan plan;
  if (start.has_chirp) {
    plan.chirp.f0Hz = start.chirp.f0_hz;
    plan.chirp.f1Hz = start.chirp.f1_hz;
    plan.chirp.durationS = static_cast<double>(start.chirp.duration_us) / 1e6;
    plan.chirp.fadeS = static_cast<double>(start.chirp.fade_us) / 1e6;
    // a shape no sweep can be: the one before rounds
    if (plan.chirp.f0Hz <= 0 || plan.chirp.f1Hz <= 0 || plan.chirp.durationS <= 0 || plan.chirp.durationS > 1 ||
        plan.chirp.fadeS * 2 > plan.chirp.durationS)
      plan.chirp = calibration::legacyChirp();
  } else {
    plan.chirp = calibration::legacyChirp();
  }
  plan.probe = calibration::chirpProbe(plan.chirp);
  if (start.has_probe && start.probe.kind == cadence_sync_v1_ProbeKind_PROBE_KIND_GOLD &&
      start.probe.band_lo_hz > 0 && start.probe.band_hi_hz > start.probe.band_lo_hz) {
    plan.probe.kind = calibration::ProbeKind::Gold;
    plan.probe.bandLoHz = start.probe.band_lo_hz;
    plan.probe.bandHiHz = start.probe.band_hi_hz;
    plan.probe.fadeS = static_cast<double>(start.probe.fade_us) / 1e6;
  }
  const std::size_t count = std::min<std::size_t>(start.slots_count, calibration::kMaxSlots);
  std::size_t rounds = std::max<std::uint32_t>(start.rounds, 1);
  // round-major, the same members every round: a start that is not is taken as one round of its first members
  std::size_t members = count / rounds;
  bool layout = rounds <= count && count % rounds == 0 && members <= calibration::kMaxMembers;
  for (std::size_t k = 0; layout && k < count; ++k)
    layout = textOf(start.slots[k].member_id) == textOf(start.slots[k % members].member_id);
  if (!layout) {
    rounds = 1;
    members = std::min(count, calibration::kMaxMembers);
  }
  plan.rounds = static_cast<std::uint32_t>(rounds);
  for (std::size_t k = 0; k < members; ++k)
    plan.members.push_back(textOf(start.slots[k].member_id));
  plan.priorNs.assign(members, 0);
  plan.priorSpanNs.assign(members, 150'000'000);
  for (pb_size_t k = 0; k < start.priors_count; ++k) {
    const auto found = std::find(plan.members.begin(), plan.members.end(), textOf(start.priors[k].member_id));
    if (found == plan.members.end())
      continue;
    const auto i = static_cast<std::size_t>(found - plan.members.begin());
    plan.priorNs[i] = static_cast<std::int64_t>(start.priors[k].prior_us) * 1000;
    plan.priorSpanNs[i] = static_cast<std::int64_t>(start.priors[k].prior_span_us) * 1000;
  }
  plan.toleranceNs = static_cast<std::int64_t>(start.tolerance_us == 0 ? 2000 : start.tolerance_us) * 1000;
  for (std::size_t k = 0; k < rounds * members; ++k) {
    plan.slotNs.push_back(start.slots[k].t_ns);
    plan.gainDb.push_back(std::min(start.slots[k].gain_db, calibration::kMaxGainDb));
    if (plan.probe.kind == calibration::ProbeKind::Gold)
      plan.probeCodes.push_back(start.slots[k].code);
  }
  if (plan.probe.kind == calibration::ProbeKind::Gold && start.has_ultrasonic && start.ultrasonic.has_probe &&
      start.ultrasonic.probe.kind == cadence_sync_v1_ProbeKind_PROBE_KIND_GOLD &&
      start.ultrasonic.probe.band_lo_hz >= 16000 && start.ultrasonic.probe.band_hi_hz > start.ultrasonic.probe.band_lo_hz) {
    plan.ultrasonic = calibration::ultrasonicProbe();
    plan.ultrasonic->bandLoHz = start.ultrasonic.probe.band_lo_hz;
    plan.ultrasonic->bandHiHz = start.ultrasonic.probe.band_hi_hz;
    plan.ultrasonic->fadeS = static_cast<double>(start.ultrasonic.probe.fade_us) / 1e6;
    plan.ultrasonicSlotNs.assign(members, 0);
    plan.ultrasonicCodes.resize(members);
    for (pb_size_t k = 0; k < start.ultrasonic.slots_count; ++k) {
      const auto& slot = start.ultrasonic.slots[k];
      const auto found = std::find(plan.members.begin(), plan.members.end(), textOf(slot.member_id));
      if (found == plan.members.end())
        continue;
      const auto i = static_cast<std::size_t>(found - plan.members.begin());
      plan.ultrasonicSlotNs[i] = slot.t_ns;
      plan.ultrasonicCodes[i] = slot.code;
    }
  }
  return plan;
}

cadence_sync_v1_CalibrateStart startOf(const calibration::Plan& plan, std::uint32_t id, std::uint32_t epoch) {
  cadence_sync_v1_CalibrateStart start = cadence_sync_v1_CalibrateStart_init_zero;
  start.id = id;
  start.epoch = epoch;
  start.from_ns = plan.windowFromNs();
  start.to_ns = plan.windowToNs();
  start.rounds = plan.rounds;
  start.has_chirp = true;
  start.accepts_progress = true;
  start.chirp.f0_hz = static_cast<std::uint32_t>(std::lround(plan.chirp.f0Hz));
  start.chirp.f1_hz = static_cast<std::uint32_t>(std::lround(plan.chirp.f1Hz));
  start.chirp.duration_us = static_cast<std::uint32_t>(std::lround(plan.chirp.durationS * 1e6));
  start.chirp.fade_us = static_cast<std::uint32_t>(std::lround(plan.chirp.fadeS * 1e6));
  if (plan.probe.kind == calibration::ProbeKind::Gold) {
    start.has_probe = true;
    start.probe.kind = cadence_sync_v1_ProbeKind_PROBE_KIND_GOLD;
    start.probe.band_lo_hz = static_cast<std::uint32_t>(std::lround(plan.probe.bandLoHz));
    start.probe.band_hi_hz = static_cast<std::uint32_t>(std::lround(plan.probe.bandHiHz));
    start.probe.fade_us = static_cast<std::uint32_t>(std::lround(plan.probe.fadeS * 1e6));
    start.tolerance_us = static_cast<std::uint32_t>(std::clamp<std::int64_t>(plan.toleranceNs / 1000, 0, 4'000'000'000LL));
    constexpr auto kPriors = sizeof start.priors / sizeof start.priors[0];
    for (std::size_t k = 0; k < plan.members.size() && start.priors_count < kPriors; ++k) {
      auto& prior = start.priors[start.priors_count++];
      copyText(prior.member_id, plan.members[k]);
      prior.prior_us = static_cast<std::int32_t>(std::clamp<std::int64_t>(plan.prior(k) / 1000, -2'000'000'000LL,
                                                                         2'000'000'000LL));
      prior.prior_span_us = static_cast<std::uint32_t>(
          std::clamp<std::int64_t>(plan.priorSpan(k) / 1000, 0, 4'000'000'000LL));
    }
  }
  constexpr auto kSlots = sizeof start.slots / sizeof start.slots[0];
  for (std::size_t k = 0; k < plan.slotNs.size() && start.slots_count < kSlots; ++k) {
    auto& slot = start.slots[start.slots_count++];
    copyText(slot.member_id, plan.members[k % plan.members.size()]);
    slot.t_ns = plan.slotNs[k];
    slot.gain_db = plan.gain(k);
    slot.code = plan.probeCode(k);
  }
  if (plan.hasUltrasonic()) {
    start.has_ultrasonic = true;
    start.ultrasonic.has_probe = true;
    start.ultrasonic.probe.kind = cadence_sync_v1_ProbeKind_PROBE_KIND_GOLD;
    start.ultrasonic.probe.band_lo_hz = static_cast<std::uint32_t>(std::lround(plan.ultrasonic->bandLoHz));
    start.ultrasonic.probe.band_hi_hz = static_cast<std::uint32_t>(std::lround(plan.ultrasonic->bandHiHz));
    start.ultrasonic.probe.fade_us = static_cast<std::uint32_t>(std::lround(plan.ultrasonic->fadeS * 1e6));
    for (std::size_t i = 0; i < plan.members.size(); ++i) {
      if (plan.ultrasonicSlotNs[i] == 0)
        continue;
      auto& slot = start.ultrasonic.slots[start.ultrasonic.slots_count++];
      copyText(slot.member_id, plan.members[i]);
      slot.t_ns = plan.ultrasonicSlotNs[i];
      slot.code = plan.ultrasonicCodes[i];
    }
  }
  return start;
}

calibration::Plan planOf(const cadence_sync_v1_TrackCycle& cycle) {
  calibration::Plan plan;
  plan.probe = calibration::ultrasonicProbe();
  plan.rounds = 1;
  plan.toleranceNs = 2'000'000;
  for (pb_size_t k = 0; k < cycle.slots_count && plan.members.size() < calibration::kMaxMembers; ++k) {
    plan.members.push_back(textOf(cycle.slots[k].member_id));
    plan.slotNs.push_back(cycle.slots[k].t_ns);
    plan.probeCodes.push_back(cycle.slots[k].code);
  }
  plan.priorNs.assign(plan.members.size(), 0);
  plan.priorSpanNs.assign(plan.members.size(), 150'000'000);
  return plan;
}

cadence_sync_v1_TrackCycle cycleOf(const calibration::Plan& plan, std::uint32_t id, std::uint32_t epoch) {
  cadence_sync_v1_TrackCycle out = cadence_sync_v1_TrackCycle_init_zero;
  out.id = id;
  out.epoch = epoch;
  out.from_ns = plan.windowFromNs();
  out.to_ns = plan.windowToNs();
  for (std::size_t i = 0; i < plan.members.size() && out.slots_count < calibration::kMaxMembers; ++i) {
    auto& slot = out.slots[out.slots_count++];
    copyText(slot.member_id, plan.members[i]);
    slot.t_ns = plan.slot(0, i);
    slot.code = plan.probeCode(i);
  }
  return out;
}

cadence_sync_v1_TrackArrivals trackOf(const cadence_sync_v1_CalibrateArrivals& arrivals) {
  cadence_sync_v1_TrackArrivals out = cadence_sync_v1_TrackArrivals_init_zero;
  out.id = arrivals.id;
  copyText(out.code, textOf(arrivals.code));
  out.channels = arrivals.channels;
  out.arrivals_count = arrivals.arrivals_count;
  for (pb_size_t i = 0; i < arrivals.arrivals_count; ++i)
    out.arrivals[i] = arrivals.arrivals[i];
  return out;
}

cadence_sync_v1_CalibrateArrivals calibrateOf(const cadence_sync_v1_TrackArrivals& arrivals) {
  cadence_sync_v1_CalibrateArrivals out = cadence_sync_v1_CalibrateArrivals_init_zero;
  out.id = arrivals.id;
  copyText(out.code, textOf(arrivals.code));
  out.channels = arrivals.channels;
  out.arrivals_count = arrivals.arrivals_count;
  for (pb_size_t i = 0; i < arrivals.arrivals_count; ++i)
    out.arrivals[i] = arrivals.arrivals[i];
  return out;
}

namespace {

bool place(GroupPlayout& playout, std::uint32_t epoch, const calibration::Plan& plan, const std::string& member,
           bool coded, std::uint64_t seqBase = 0) {
  const auto found = std::find(plan.members.begin(), plan.members.end(), member);
  if (found == plan.members.end() || plan.slotNs.empty())
    return false;
  const auto k = static_cast<std::size_t>(found - plan.members.begin());
  if (plan.skipped(k))
    return false;
  struct Mine {
    std::int64_t slotNs;
    calibration::ChirpShape chirp;
    calibration::ProbeShape probe;
    std::uint32_t code;
    std::shared_ptr<const calibration::ProbeTable> table;
  };
  std::vector<Mine> mine;
  for (std::size_t index = k; index < plan.slotNs.size(); index += plan.members.size()) {
    Mine slot;
    slot.slotNs = plan.slotNs[index];
    slot.code = plan.probeCode(index);
    slot.chirp = calibration::withGain(plan.chirp, plan.gain(index));
    slot.probe = plan.probe;
    slot.probe.amplitude *= std::pow(10.0, static_cast<double>(plan.gain(index)) / 20.0);
    if (coded)
      slot.table = std::make_shared<const calibration::ProbeTable>(slot.probe, slot.code);
    mine.push_back(std::move(slot));
  }
  playout.format(epoch, MusicCodec::PcmS16);
  MusicEncoder encoder(MusicCodec::PcmS16, 0);
  const auto trim = playout.trimNs();
  const auto probeNs = static_cast<std::int64_t>(
      std::ceil((coded ? calibration::probeDuration(plan.probe) : plan.chirp.durationS) * 1e9));
  const auto after = std::max(kChirpAfterNs, probeNs + 100'000'000);
  const auto start = mine.front().slotNs - kChirpBeforeNs;
  const auto end = mine.back().slotNs + after;
  // one contiguous run: a gap in the frames' times would read as a lost stretch to the playout
  const auto frames = static_cast<std::uint64_t>((end - start + kGroupFrameNs - 1) / kGroupFrameNs);
  std::vector<float> pcm(kGroupFrameSamples * kGroupChannels);
  std::vector<std::uint8_t> bytes;
  std::size_t next = 0;
  for (std::uint64_t seq = 0; seq < frames; ++seq) {
    const auto t = start + static_cast<std::int64_t>(seq) * kGroupFrameNs;
    while (next + 1 < mine.size() && t >= mine[next].slotNs + probeNs)
      ++next;
    const auto& mineNow = mine[next];
    for (std::size_t n = 0; n < kGroupFrameSamples; ++n) {
      const double at = static_cast<double>(t - mineNow.slotNs) / 1e9 + static_cast<double>(n) / kGroupRate;
      const auto v = static_cast<float>(coded ? mineNow.table->at(at)
                                              : calibration::chirpAt(mineNow.chirp, at));
      pcm[2 * n] = v;
      pcm[2 * n + 1] = v;
    }
    if (!encoder.encode(pcm, bytes))
      return false;
    playout.frame(epoch, seqBase + seq, t - trim, 0, bytes);
  }
  return true;
}

}  // namespace

bool placeChirps(GroupPlayout& playout, std::uint32_t epoch, const calibration::Plan& plan, const std::string& member) {
  return place(playout, epoch, plan, member, false);
}

bool placeProbes(GroupPlayout& playout, std::uint32_t epoch, const calibration::Plan& plan, const std::string& member) {
  const bool audible = place(playout, epoch, plan, member, plan.probe.kind == calibration::ProbeKind::Gold);
  if (!plan.hasUltrasonic())
    return audible;
  const auto round = plan.ultrasonicPlan();
  auto first = std::int64_t{0};
  for (const auto slot : round.slotNs)
    if (slot > 0 && (first == 0 || slot < first))
      first = slot;
  if (first == 0)
    return audible;
  const auto seqBase = static_cast<std::uint64_t>((first - plan.slotNs.front()) / kGroupFrameNs);
  const bool ultrasonic = place(playout, epoch, round, member, true, seqBase);
  return audible || ultrasonic;
}

cadence_sync_v1_CalibrateArrivals refusal(std::uint32_t id, std::string_view code) {
  cadence_sync_v1_CalibrateArrivals out = cadence_sync_v1_CalibrateArrivals_init_zero;
  out.id = id;
  copyText(out.code, code);
  return out;
}

cadence_sync_v1_CalibrateArrivals arrivalsOf(std::uint32_t id, const calibration::Plan& plan,
                                             const std::string& member, const MicResult& mic,
                                             const std::function<double(double monoNs)>& toLeader) {
  if (!mic.code.empty())
    return refusal(id, mic.code);
  const auto& rec = mic.recording;
  if (rec.pcm.empty() || rec.channels == 0)
    return refusal(id, "no_capture");
  const auto clock = calibration::fitSampleClock(rec.stamps, rec.rate);
  auto out = refusal(id, "");
  out.channels = static_cast<std::uint32_t>(rec.channels);
  if (plan.probe.kind == calibration::ProbeKind::Gold) {
    const auto self = std::find(plan.members.begin(), plan.members.end(), member);
    const auto selfIndex = self == plan.members.end() ? plan.members.size()
                                                      : static_cast<std::size_t>(self - plan.members.begin());
    calibration::AnalysisConfig config;
    config.toleranceNs = static_cast<double>(plan.toleranceNs);
    const auto heard = calibration::analyse(plan, rec.pcm, rec.channels, rec.rate, selfIndex, clock, toLeader, config);
    for (std::size_t i = 0; i < heard.size() && out.arrivals_count < calibration::kMaxMembers; ++i) {
      auto& a = out.arrivals[out.arrivals_count++];
      copyText(a.from_member_id, plan.members[i]);
      a.code = wireCode(heard[i].code);
      a.t_ns = heard[i].tNs;
      a.confidence = static_cast<float>(heard[i].confidence);
      a.spread_ns = unsignedNs(heard[i].spreadNs);
      a.rounds = heard[i].rounds;
      a.relative_ns = std::llround(heard[i].relativeNs);
      a.has_relative = heard[i].hasRelative;
      a.relative_spread_ns = unsignedNs(heard[i].relativeSpreadNs);
      a.sigma_ns = unsignedNs(heard[i].sigmaNs);
      a.ci95_ns = unsignedNs(heard[i].ci95Ns);
    }
    if (plan.hasUltrasonic()) {
      const auto round = plan.ultrasonicPlan();
      const auto extra = calibration::analyse(round, rec.pcm, rec.channels, rec.rate, selfIndex, clock, toLeader, config);
      for (std::size_t i = 0; i < extra.size() && out.ultrasonic_count < calibration::kMaxMembers; ++i) {
        auto& a = out.ultrasonic[out.ultrasonic_count++];
        copyText(a.from_member_id, round.members[i]);
        a.code = wireCode(extra[i].code);
        a.t_ns = extra[i].tNs;
        a.confidence = static_cast<float>(extra[i].confidence);
        a.spread_ns = unsignedNs(extra[i].spreadNs);
        a.rounds = extra[i].rounds;
        a.relative_ns = std::llround(extra[i].relativeNs);
        a.has_relative = extra[i].hasRelative;
        a.relative_spread_ns = unsignedNs(extra[i].relativeSpreadNs);
        a.sigma_ns = unsignedNs(extra[i].sigmaNs);
        a.ci95_ns = unsignedNs(extra[i].ci95Ns);
      }
    }
    return out;
  }
  const auto heard = calibration::analyse(plan, rec.pcm, rec.channels, rec.rate, clock, toLeader);
  for (std::size_t i = 0; i < heard.size() && out.arrivals_count < calibration::kMaxMembers; ++i) {
    auto& a = out.arrivals[out.arrivals_count++];
    copyText(a.from_member_id, plan.members[i]);
    a.code = wireCode(heard[i].code);
    a.t_ns = heard[i].tNs;
    a.confidence = static_cast<float>(heard[i].confidence);
    a.spread_ns = unsignedNs(heard[i].spreadNs);
    a.rounds = heard[i].rounds;
  }
  return out;
}

cadence_sync_v1_CalibrateArrivals analyseRecording(
    std::uint32_t id, const calibration::Plan& plan, const std::string& member, const MicResult& mic,
    const std::function<double(double monoNs)>& toLeader,
    const std::function<void(const CalibrationAnalysis&)>& analysed) {
  const auto started = std::chrono::steady_clock::now();
  auto arrivals = arrivalsOf(id, plan, member, mic, toLeader);
  if (analysed) {
    CalibrationAnalysis note;
    note.id = id;
    note.ms = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - started).count();
    note.frames = mic.recording.frames();
    note.rate = static_cast<std::uint32_t>(std::lround(mic.recording.rate));
    note.channels = mic.recording.channels;
    note.code = textOf(arrivals.code);
    if (note.code.empty())
      note.code = "ok";
    analysed(note);
  }
  return arrivals;
}

CalibrationOutcome refused(std::string_view code) {
  CalibrationOutcome out;
  out.code = std::string(code);
  return out;
}

CalibrationCollector::CalibrationCollector(std::uint32_t id, calibration::Plan plan, std::vector<std::string> offline)
    : id_(id),
      plan_(std::move(plan)),
      offline_(std::move(offline)),
      answered_(plan_.members.size(), false),
      recorded_(plan_.members.size(), false),
      budgetsNs_(plan_.members.size(), kCalibrationAnswerNs),
      answeredMs_(plan_.members.size(), -1),
      codes_(plan_.members.size()),
      channels_(plan_.members.size(), 0),
      heard_(plan_.members.size(), std::vector<calibration::Heard>(plan_.members.size())),
      heardV2_(plan_.members.size(), std::vector<calibration::HeardV2>(plan_.members.size())),
      ultrasonic_(plan_.members.size(), std::vector<calibration::HeardV2>(plan_.members.size())),
      ultrasonicAnswered_(plan_.members.size(), false) {}

bool CalibrationCollector::add(const std::string& member, const cadence_sync_v1_CalibrateArrivals& arrivals,
                               std::int64_t atNs) {
  if (arrivals.id != id_)
    return false;
  const auto found = std::find(plan_.members.begin(), plan_.members.end(), member);
  if (found == plan_.members.end())
    return false;
  const auto j = static_cast<std::size_t>(found - plan_.members.begin());
  answered_[j] = true;
  const auto answeredMs = (atNs - plan_.windowToNs()) / 1'000'000;
  answeredMs_[j] = static_cast<std::int32_t>(
      std::clamp<std::int64_t>(answeredMs, 0, std::numeric_limits<std::int32_t>::max()));
  codes_[j] = textOf(arrivals.code);
  channels_[j] = arrivals.channels;
  ultrasonicAnswered_[j] = arrivals.ultrasonic_count > 0;
  for (pb_size_t k = 0; k < arrivals.arrivals_count; ++k) {
    const auto& a = arrivals.arrivals[k];
    const auto from = std::find(plan_.members.begin(), plan_.members.end(), textOf(a.from_member_id));
    if (from == plan_.members.end())
      continue;
    const auto source = static_cast<std::size_t>(from - plan_.members.begin());
    if (plan_.probe.kind == calibration::ProbeKind::Gold) {
      auto& h = heardV2_[j][source];
      h.code = fromWireV2(a.code);
      h.tNs = a.t_ns;
      h.confidence = std::clamp(static_cast<double>(a.confidence), 0.0, 1.0);
      h.spreadNs = a.spread_ns;
      h.rounds = a.rounds;
      h.relativeNs = static_cast<double>(a.relative_ns);
      h.hasRelative = a.has_relative;
      h.relativeSpreadNs = a.relative_spread_ns;
      h.sigmaNs = a.sigma_ns;
      h.ci95Ns = a.ci95_ns;
      continue;
    }
    auto& h = heard_[j][source];
    h.code = fromWire(a.code);
    h.tNs = a.t_ns;
    h.confidence = std::clamp(static_cast<double>(a.confidence), 0.0, 1.0);
    h.spreadNs = a.spread_ns;
    h.rounds = a.rounds;
  }
  for (pb_size_t k = 0; k < arrivals.ultrasonic_count; ++k) {
    const auto& a = arrivals.ultrasonic[k];
    const auto from = std::find(plan_.members.begin(), plan_.members.end(), textOf(a.from_member_id));
    if (from == plan_.members.end())
      continue;
    const auto source = static_cast<std::size_t>(from - plan_.members.begin());
    auto& h = ultrasonic_[j][source];
    h.code = fromWireV2(a.code);
    h.tNs = a.t_ns;
    h.confidence = std::clamp(static_cast<double>(a.confidence), 0.0, 1.0);
    h.spreadNs = a.spread_ns;
    h.rounds = a.rounds;
    h.relativeNs = static_cast<double>(a.relative_ns);
    h.hasRelative = a.has_relative;
    h.relativeSpreadNs = a.relative_spread_ns;
    h.sigmaNs = a.sigma_ns;
    h.ci95Ns = a.ci95_ns;
  }
  return true;
}

bool CalibrationCollector::recorded(const std::string& member,
                                    const cadence_sync_v1_CalibrateProgress& progress) {
  if (progress.id != id_ || progress.stage != cadence_sync_v1_CalibrateStage_CALIBRATE_STAGE_RECORDED)
    return false;
  const auto found = std::find(plan_.members.begin(), plan_.members.end(), member);
  if (found == plan_.members.end())
    return false;
  const auto j = static_cast<std::size_t>(found - plan_.members.begin());
  recorded_[j] = true;
  budgetsNs_[j] = static_cast<std::int64_t>(
                      std::min(progress.analysis_budget_ms, kCalibrationMaxAnalysisBudgetMs)) *
                  1'000'000;
  return true;
}

bool CalibrationCollector::complete() const {
  return std::all_of(answered_.begin(), answered_.end(), [](bool a) { return a; });
}

std::int64_t CalibrationCollector::deadlineNs() const {
  auto deadline = plan_.windowToNs();
  for (std::size_t i = 0; i < answered_.size(); ++i)
    if (!answered_[i])
      deadline = std::max(deadline, plan_.windowToNs() + budgetsNs_[i]);
  return deadline;
}

CalibrationOutcome CalibrationCollector::finish() const {
  CalibrationOutcome out;
  const auto solution = plan_.probe.kind == calibration::ProbeKind::Gold ? calibration::solveV2(plan_, heardV2_)
                                                                         : calibration::solve(plan_, heard_);
  out.code = calibration::token(solution.code);
  out.residualUs = static_cast<std::uint32_t>(std::min(solution.residualNs / 1000.0, 4e9));
  bool anyNative = false;
  if (plan_.hasUltrasonic())
    for (std::size_t recorder = 0; recorder < plan_.members.size(); ++recorder)
      if (ultrasonicAnswered_[recorder])
        for (const auto& heard : ultrasonic_[recorder])
          anyNative = anyNative || heard.code != calibration::ArrivalCodeV2::Unsupported;
  bool all = true;
  for (std::size_t i = 0; i < plan_.members.size(); ++i) {
    CalibrationMemberOutcome m;
    m.id = plan_.members[i];
    m.micChannels = channels_[i];
    m.answeredMs = answeredMs_[i];
    if (plan_.hasUltrasonic()) {
      if (plan_.ultrasonicSlotNs[i] == 0 || !anyNative) {
        m.ultrasonic = "unsupported";
      } else {
        bool ok = false, weak = false;
        for (std::size_t recorder = 0; recorder < plan_.members.size(); ++recorder) {
          if (!ultrasonicAnswered_[recorder])
            continue;
          const auto& heard = ultrasonic_[recorder][i];
          ok = ok || (heard.code == calibration::ArrivalCodeV2::Ok && heard.confidence >= 0.5);
          weak = weak || heard.code == calibration::ArrivalCodeV2::Unstable ||
                 (heard.code == calibration::ArrivalCodeV2::Ok && heard.confidence < 0.5) ||
                 heard.code == calibration::ArrivalCodeV2::Weak;
        }
        m.ultrasonic = ok ? "ok" : weak ? "weak" : "missed";
      }
    }
    const auto& s = solution.members[i];
    if (!answered_[i])
      m.code = recorded_[i] ? "analysis_timeout" : "no_answer";
    else if (!codes_[i].empty())
      m.code = codes_[i];
    else if (plan_.probe.kind == calibration::ProbeKind::Gold && s.code != calibration::MemberCode::Ok) {
      // a silent source's code is absent everywhere; it must never inherit a louder neighbour's arrival
      bool missed = false;
      bool heardOk = false;
      for (const auto& recorder : heardV2_) {
        missed = missed || recorder[i].code == calibration::ArrivalCodeV2::Missed;
        heardOk = heardOk || recorder[i].code == calibration::ArrivalCodeV2::Ok;
      }
      m.code = missed && !heardOk ? "missed" : calibration::token(s.code);
    } else {
      m.code = calibration::token(s.code);
    }
    if (s.code == calibration::MemberCode::Ok && answered_[i] && codes_[i].empty()) {
      m.offsetUs = static_cast<std::int32_t>(std::clamp(std::llround(s.offsetNs / 1000.0), -2'000'000LL, 2'000'000LL));
      m.confidence = static_cast<float>(s.confidence);
      m.spreadUs = static_cast<std::uint32_t>(std::min(s.spreadNs / 1000.0, 4e9));
      m.ci95Us = static_cast<std::uint32_t>(std::min(s.ci95Ns / 1000.0, 4e9));
      m.rounds = s.rounds;
    } else {
      all = false;
    }
    out.members.push_back(std::move(m));
  }
  for (const auto& id : offline_) {
    CalibrationMemberOutcome offline;
    offline.id = id;
    offline.code = "offline";
    out.members.push_back(std::move(offline));
    all = false;
  }
  if (solution.code == calibration::ResultCode::Ok && !all)
    out.code = calibration::token(calibration::ResultCode::Incomplete);
  return out;
}

void CalibrationWorker::run(std::function<void()> job) {
  std::lock_guard lock(mutex_);
  if (thread_.joinable())
    thread_.join();
  thread_ = std::thread(std::move(job));
}

void CalibrationWorker::join() {
  std::lock_guard lock(mutex_);
  if (thread_.joinable())
    thread_.join();
}

}  // namespace cadence::sync

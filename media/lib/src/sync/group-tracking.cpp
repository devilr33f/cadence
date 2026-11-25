#include "sync/group-tracking.h"

#include <algorithm>

namespace cadence::sync {

namespace {

constexpr std::int64_t kTrackLeadNs = 500'000'000;
constexpr std::int64_t kTrackAnswerNs = 3'000'000'000;

bool allowed(const GroupTrackerConfig& config, const std::string& id) {
  return std::find(config.members.begin(), config.members.end(), id) != config.members.end();
}

}  // namespace

GroupTracker::GroupTracker(GroupTrackerConfig config) : config_(std::move(config)) {
  calibration::TrackingConfig window;
  window.deadbandNs = config_.deadbandNs;
  window_ = calibration::TrackingWindow(window);
}

void GroupTracker::publish(const calibration::TrackingReport& report, bool force, const GroupTrackerEvents& events) {
  if (!force && report.state == published_)
    return;
  published_ = report.state;
  if (events.report)
    events.report(report);
}

void GroupTracker::cancel(const GroupTrackerEvents& events) {
  if (active_) {
    active_.reset();
    if (events.cancel)
      events.cancel();
  }
  nextStartNs_ = 0;
  window_.reset();
}

void GroupTracker::tick(std::int64_t now, std::uint32_t epoch, bool playing, bool voice,
                        const std::vector<LeaderMemberView>& views, const GroupTrackerEvents& events) {
  if (!config_.enabled) {
    cancel(events);
    publish({.state = "off", .members = {}}, false, events);
    return;
  }
  const bool legacy = std::any_of(views.begin(), views.end(), [](const LeaderMemberView& view) {
    return view.connected && view.calibrationVersion < kTrackingVersion;
  });
  std::vector<std::string> ids{config_.memberId};
  for (const auto& view : views) {
    if (!view.connected || !allowed(config_, view.id))
      continue;
    if (view.status && std::string_view(view.status->output_kind) == "bluetooth")
      continue;
    ids.push_back(view.id);
  }
  const bool unsupported = legacy || !allowed(config_, config_.memberId) || config_.outputKind == "bluetooth" ||
                           ids.size() < 2;
  if (unsupported) {
    cancel(events);
    publish({.state = "unsupported", .members = {}}, false, events);
    return;
  }
  if (!playing || voice) {
    cancel(events);
    publish({.state = "idle", .members = {}}, false, events);
    return;
  }

  if (active_ && (active_->collector.complete() || now >= active_->deadlineNs)) {
    auto finished = std::move(*active_);
    active_.reset();
    if (events.cycle)
      events.cycle(finished.collector.id(), static_cast<std::uint32_t>(finished.collector.plan().members.size()),
                   finished.collector.answers());
    publish(window_.add(finished.collector.plan(), finished.collector.observations(), now), true, events);
  }
  if (active_ || now < nextStartNs_)
    return;
  auto plan = calibration::makeProbePlan(ids, now + kTrackLeadNs, {}, {}, calibration::kSlotSpacingNs, 1);
  plan.probe = calibration::ultrasonicProbe();
  plan.toleranceNs = 2'000'000;
  const auto id = ++cycles_;
  active_.emplace(Active{CalibrationCollector(id, plan, {}), plan.windowToNs() + kTrackAnswerNs});
  nextStartNs_ = plan.slot(0, 0) + config_.cyclePeriodNs;
  if (events.start)
    events.start(id, epoch, plan);
}

void GroupTracker::arrivals(const std::string& member, const cadence_sync_v1_TrackArrivals& arrivals,
                            std::int64_t now) {
  if (active_)
    active_->collector.add(member, calibrateOf(arrivals), now);
}

}  // namespace cadence::sync

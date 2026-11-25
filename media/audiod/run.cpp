#include "audiod/run.h"

#include "audiod/bluetooth-handlers.h"
#include "audiod/capture.h"
#include "audiod/child-logs.h"
#include "audiod/daemon.h"
#include "audiod/group-handlers.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-handlers.h"
#include "audiod/media-source.h"
#include "audiod/playback.h"
#include "audiod/tap-server.h"
#include "audiod/traces.h"
#ifdef CADENCE_MEDIA_SOLOIST
#include "audiod/soloist-handlers.h"
#include "audiod/soloist-jail.h"
#endif
#include "audiod/vendor-probe.h"

#ifdef CADENCE_MEDIA_AIRPLAY
#include "audiod/airplay-handlers.h"
#endif
#ifdef CADENCE_MEDIA_AIRPLAY_UXPLAY
#include "audiod/airplay-uxplay-handlers.h"
#endif
#ifdef CADENCE_MEDIA_URL
#include "url/ring-stream.h"
#include "url/url-source.h"
#endif

#include <chrono>
#include <cstddef>
#include <cstdio>
#include <cstdlib>
#include <functional>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr int kExitFailed = 1;

}  // namespace

// the daemon and its parts until the loop stops; they are gone when it returns, before the children's lines they send
int runDaemon(Loop& loop, IpcServer& ipc, const Log& log) {
  const ChildLogs childLogs(loop, ipc, log);
  // before the daemon, so it outlives the parts and the playback thread that mark through it
  Traces traces(loop, ipc, log);
  Daemon daemon(ipc, log, compiledBackends());
  auto playback = std::make_unique<Playback>(loop, ipc, log);
  playback->traceTo(traces);
#ifdef CADENCE_MEDIA_URL
  // the decoder threads start later, from the loop thread, with its blocked signals
  auto urls =
      cadence::url::urlSourceFactory(log, [&loop](std::function<void()> task) { loop.post(std::move(task)); });
  // a ring's url (an alarm's music, a reminder's speech): after the url factory, which set ffmpeg and openssl up
  playback->useRingStreams(cadence::url::ringStreamFactory());
#else
  MediaSourceFactory urls = refuseUrls;
#endif
#ifdef CADENCE_MEDIA_AO_RING
  RingFactory rings = sizedRing;
#else
  RingFactory rings = noRings;
#endif
  auto media = std::make_unique<MediaHandlers>(loop, ipc, log, *playback, std::move(urls), unixNowMs, kUrlPlayBound,
                                               std::move(rings));
  auto tap = std::make_unique<TapServer>(loop, ipc, log, *playback);
  tap->useRings(media->externals());
  // multiroom: its playouts push into the music input, and a leader retargets the sessions onto its feed
  auto group = std::make_unique<GroupHandlers>(loop, ipc, log, *playback, *media);
  // a follower screen's picture follows the group's media clock (multiroom P4); the daemon drops its parts only once
  // the loop is done, so the tap never asks a group that went
  tap->useGroupClock([g = group.get()](std::int64_t nowMono) { return g->heardClock(nowMono); });
#ifdef CADENCE_MEDIA_SOLOIST
  SoloistOptions soloistOptions;
  soloistOptions.logs = childLogs.target();
  auto soloist = std::make_unique<SoloistHandlers>(loop, ipc, log, *playback, *media, std::move(soloistOptions));
#endif
#ifdef CADENCE_MEDIA_AIRPLAY
  AirplayOptions airplayOptions;
  airplayOptions.logs = childLogs.target();
  auto airplay = std::make_unique<AirplayHandlers>(loop, ipc, log, *playback, *media, std::move(airplayOptions));
#endif
#ifdef CADENCE_MEDIA_AIRPLAY_UXPLAY
  auto uxplay = std::make_unique<uxplay::UxplayHandlers>(loop, ipc, log, *playback, *media, *tap);
#endif
  // a phone's a2dp pcm, whose fd cadenced hands over
  auto bluetooth = std::make_unique<BluetoothHandlers>(loop, ipc, log, *playback, *media);
  auto capture = std::make_unique<Capture>(loop, ipc, log);
  capture->traceTo(traces);
  // a group calibration records the microphone; the group's part goes before the capture's
  group->useMic([mic = capture.get()](std::int64_t from, std::int64_t to, std::function<void(sync::MicResult)> done) {
    mic->record(from, to, std::move(done));
  }, [mic = capture.get()](std::int64_t from, std::int64_t to, std::function<void(sync::MicResult)> done) {
    mic->recordNative(from, to, std::move(done));
  }, [mic = capture.get()] { mic->cancelNative(); });
  daemon.add(std::move(playback));
  daemon.add(std::move(capture));
  daemon.add(std::move(media));
  daemon.add(std::move(tap));
  daemon.add(std::move(group));
  // last, so they stop first: soloist's pcm, shairport's pipe and the phone's pcm push into playback's music input
  daemon.add(std::move(bluetooth));
#ifdef CADENCE_MEDIA_SOLOIST
  daemon.add(std::move(soloist));
#endif
#ifdef CADENCE_MEDIA_AIRPLAY
  daemon.add(std::move(airplay));
#endif
#ifdef CADENCE_MEDIA_AIRPLAY_UXPLAY
  daemon.add(std::move(uxplay));
#endif

  log.info("audiod_start", {{"version", kVersion}});
#ifdef CADENCE_MEDIA_SOLOIST
  if constexpr (!kSoloistJailed)
    log.warn("soloist_unjailed");
#endif
#ifdef CADENCE_MEDIA_AIRPLAY
  if constexpr (!kAirplayLan)
    log.warn("airplay_lan_off");
#endif
  if (const auto error = loop.run()) {
    log.error("loop_failed", {{"call", error->call}, {"errno", error->code}});
    return kExitFailed;
  }
  return 0;
}

}  // namespace cadence::audiod

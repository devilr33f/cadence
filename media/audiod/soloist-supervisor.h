#pragma once

#include "audiod/child-log.h"
#include "audiod/jailed-process.h"
#include "audiod/log.h"
#include "audiod/media-source.h"
#include "audiod/soloist-control.h"
#include "audiod/soloist-launch.h"
#include "audiod/soloist-pcm.h"
#include "audiod/soloist-version.h"
#include "backend/pcm-input.h"

#include <chrono>
#include <cstdint>
#include <functional>
#include <memory>
#include <string>
#include <string_view>

namespace cadence::audiod {

enum class SoloistState : std::uint8_t { Off, KeyMissing, NoBuild, Starting, Ready, Expired, Failed };

// what SoloistStatus carries; never the key
struct SoloistStatus {
  SoloistState state = SoloistState::Off;
  // a fixed token: missing|invalid with KeyMissing; ca_missing, runtime_missing, scratch_failed, pcm_failed,
  // spawn_failed or auth_failed with Failed
  std::string code;
  // of the running build, once a probe at its start read them
  std::string version;
  std::int64_t buildEpoch = 0;
  // the last exit: its code, -1 for a signal, 0 for none yet
  std::int32_t exitCode = 0;
  // starts in a row that ended without a websocket answer; cadenced rolls a new build back on them
  std::uint32_t silentStarts = 0;

  bool operator==(const SoloistStatus&) const = default;
};

// v1's schedule (reference §4.1, §7); tests shorten it
struct SoloistTimings {
  std::chrono::milliseconds portTick = soloist::kPortTick;
  std::chrono::milliseconds tick = soloist::kTick;
  std::chrono::milliseconds portWait = soloist::kPortWait;
  std::chrono::milliseconds answerWait = soloist::kAnswerWait;
  std::chrono::milliseconds reconnect = soloist::kReconnect;
  // restart waits: the first, doubling up to the last, and from the first again after a run of steady
  std::chrono::milliseconds firstBackoff{1000};
  std::chrono::milliseconds lastBackoff{60000};
  std::chrono::milliseconds steady{60000};
  // a switch held while music plays is looked at again this often
  std::chrono::milliseconds switchCheck{60000};
  std::chrono::milliseconds probeLimit = kCaptureLimit;
};

struct SoloistOptions {
  SoloistTimings timings{};
  // the host's, bound into the jail; a symlink is resolved
  std::string resolvConf = "/etc/resolv.conf";
  SoloistPcmOptions pcm{};
  // where soloist's lines go (M6b-D7): none without one
  ChildLogTarget logs{};
};

// what the supervisor reports, each on its own thread: an implementation hands it to the loop (Loop::post) and never
// blocks
struct SoloistHooks {
  std::function<void(const SoloistStatus& status)> status;
  // a probe's answer, for the request of that id
  std::function<void(const std::string& requestId, const SoloistProbe& probe)> probed;
  // a deliberate slider move, for cadenced (SenderVolume)
  std::function<void(std::uint8_t level)> senderVolume{};
  // runs a task on the loop thread: the source's events go through it with the events handle
  std::function<void(std::function<void()>)> post{};
};

// a control command's code, kMediaOk or control_unsupported, called once on the supervisor's thread
using ControlAnswer = std::function<void(std::string_view code)>;

// soloist under audiod (M4c-D8, reference §7): one long-lived thread runs it, polling soloist's websocket, its
// output pipe, a command queue and a stop fd, with the schedule's timers. everything that blocks (a start, a stop and
// its reap, a probe) happens on that thread, never on the loop's, which only queues commands.
//
// soloist starts once the config enables it with a valid key, the playback gate is open (S0 §7.5: no media child
// before a volume held), <dir>/soloist is a regular file, the runtime and the ca bundle are there, the scratch is made
// (once per audiod run) and the pcm socket bound; a start whose build, runtime or scratch is missing waits for a new
// config or a switch. a start probes the build's version, clears ws.port and runs soloist in its jail; its port is
// polled every portTick up to portWait, and a websocket handshake by answerWait makes it ready, else the start is
// silent: killed, counted and restarted. an exit restarts it after a wait of firstBackoff doubling to lastBackoff (from
// the first again after a steady run or a switch); an exit with code 10 (its build expired) waits for a switch. a
// switch starts the installed build at once when nothing plays, else once playback stops. a websocket that closes
// while soloist runs is connected again reconnect later (soloist_session_lost). soloist's output (stdout and stderr, two
// pipes) is matched for an anchored login failed (auth_failed, once per run), and its lines go to the options' logs as
// soloist's (ChildLog), never to audiod's own log.
//
// each connection is a session (sender-volume): its playback states, track and position reach the source's events
// (playing only while logged in, and only as soloist starts playing, never again while it plays; paused or idle as
// paused; a lost activation, a logout or the end of a run that answered as ended), metadata in memory only. its
// volume moves the speaker only as a deliberate slider move (SenderVolume, at most one per 200 ms), never as our own
// level coming back or the app's stored one; the speaker's level goes to soloist as set_volume once logged in. a
// control is answered ok once soloist took it, and control_unsupported when it is not logged in or answers with an
// error within 2 s. a start whose probe took a while lets the commands that came meanwhile in first.
//
// logs carry event names, codes and counts: never the key, soloist's output or what its events say
class SoloistSupervisor {
 public:
  SoloistSupervisor(const Log& log, SoloistHooks hooks, SoloistOptions options = {});
  // stops it
  ~SoloistSupervisor();

  SoloistSupervisor(const SoloistSupervisor&) = delete;
  SoloistSupervisor& operator=(const SoloistSupervisor&) = delete;

  // before start: the source's events handle, which its reports are posted with
  void events(std::shared_ptr<MediaSourceEvents> events);
  // starts the thread
  void start();
  // bounded: soloist is killed and reaped (or left to its thread after the reap wait) and the thread joined; nothing
  // is reported after it returns
  void stop();

  // any thread; each is queued for the supervisor's thread, in order. an identical config changes nothing; a change of
  // the key, the name or a path restarts soloist
  void configure(SoloistSettings settings);
  void gate(bool open);
  void probe(std::string requestId, std::string path);
  // starts the installed build: at once when nothing plays, else once playback stops
  void switchBuild();
  // answer may be empty: the command is sent all the same
  void control(soloist::Command command, ControlAnswer answer);
  // the speaker's level (SenderVolumeSet)
  void speakerLevel(std::uint8_t level);

  // any thread: soloist's pcm into music (the session's target: the music input or a group leader's feed) until
  // closeOutput, or consumed unheard
  void openOutput(PcmInput& music);
  void closeOutput();

 private:
  class Worker;
  std::unique_ptr<Worker> worker_;
};

}  // namespace cadence::audiod

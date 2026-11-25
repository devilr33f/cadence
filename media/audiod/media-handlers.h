#pragma once

#include "audiod/daemon.h"
#include "audiod/external-sources.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-overlay.h"
#include "audiod/media-sessions.h"
#include "audiod/media-source.h"
#include "audiod/playback.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <chrono>
#include <cstdint>
#include <functional>
#include <map>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

// how long a url request waits for its source to play before it is answered ok anyway: cadenced gives up on an
// answer after 10 s
inline constexpr std::chrono::milliseconds kUrlPlayBound{8000};

// the media session over the socket. UrlPlay (mode session) and MediaCommand go to the sessions, UrlPlay (mode
// overlay) to the overlay, which makes its source with the same factory; each gets exactly one CommandResult{
// request_id, ok, code}, its id echoed as it came. a media command is answered at once, or when its source answers it
// (soloist), unless cadenced went away meanwhile. UrlPlay is answered when its
// source plays (ok), fails or is dropped (its code), or at the bound (ok), whichever comes first; a refusal (gate
// shut, the factory's code) at once. no mode is invalid, an action this build does not know is unsupported.
//
// MediaState goes out on change only, and once after every Ready, idle included, so a restarted cadenced learns the
// session; nothing before this connection's Ready. a state the client's full socket refused goes out again, the
// latest one, once it has room. a new Configure and cadenced going away shut the gate for the session and the overlay
// like a failed VolumeSet does. logs carry actions, codes and session ids: never a url or metadata. ExternalSource and
// SessionControl go to the external sources, which cadenced going away clears
class MediaHandlers final : public Part {
 public:
  MediaHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaSourceFactory factory,
                WallClock now = unixNowMs, std::chrono::milliseconds playBound = kUrlPlayBound,
                RingFactory rings = noRings);

  void configure(const Configure& configuration) override;
  void ready() override;
  void reset() override;

  // a long-lived source the sessions keep (see MediaSessions::attach); before the loop runs
  void attach(const LongLivedSource& make) { sessions_.attach(make); }
  // SenderVolumeSet{sender, level}, the speaker's level for that sender's part (at most 100); a sender no part
  // registered is dropped. before the loop runs, one handler per sender
  void onSenderLevel(cadence_audiod_v1_Sender sender, std::function<void(std::uint8_t level)> handler);
  // the sessions cadenced registers and the controls it sends (see ExternalSources)
  [[nodiscard]] ExternalSources& externals() { return externals_; }
  [[nodiscard]] MediaSessions& sessions() { return sessions_; }

  // a group leader's say in the session it owns (GroupHandlers); any of them may be empty
  struct GroupHooks {
    // before a command reaches the sessions: a leader pauses the group (and gets back the media position heard where
    // it stops, which a paused seekable source is then seeked to) or flushes it (a seek, a next, a stop)
    std::function<std::optional<std::uint64_t>(MediaAction action, std::uint64_t positionMs)> command;
    // before a url session is asked for: a new track flushes the group
    std::function<void(std::uint64_t startMs)> play;
    // the position a playing session's state carries: what the group hears, not what the decoder read
    std::function<std::optional<std::uint64_t>()> heardMs;
    // every state before it goes out: false holds it back (a leader whose group still plays what its ended session
    // left), until republish
    std::function<bool(const MediaSession& session)> published;
  };
  void setGroupHooks(GroupHooks hooks) { group_ = std::move(hooks); }
  // the sessions' state goes out again, through the hooks
  void republish() { send(sessions_.session()); }
  // a command as a MediaCommand runs it, the group's hooks included (a member's Control on its leader); done gets
  // the code, now or later
  void runCommand(MediaAction action, std::uint64_t positionMs, CommandDone done);

 private:
  // a url request the sessions or the bound answer, whichever comes first
  struct Waiting {
    std::string requestId;
    bool answered = false;
    std::optional<Loop::TimerId> bound;
  };

  void urlPlay(const cadence_audiod_v1_UrlPlay& request);
  void settle(Waiting& waiting, std::string_view mode, std::string_view code, bool atBound);
  // a failed VolumeSet, a new Configure or cadenced going away
  void gateShut();
  void command(const cadence_audiod_v1_MediaCommand& request);
  void answer(std::string_view requestId, std::string_view code);
  void send(const MediaSession& session);

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  std::chrono::milliseconds playBound_;
  MediaSessions sessions_;
  MediaOverlay overlay_;
  // after the sessions it registers into, so it goes first
  ExternalSources externals_;
  std::string caFile_;
  // this connection got its Ready
  bool announced_ = false;
  // counts connections, so an answer that comes later reaches only the cadenced that asked
  std::uint64_t connection_ = 0;
  // a state the socket refused goes out again
  std::optional<Loop::TimerId> resend_;
  std::map<cadence_audiod_v1_Sender, std::function<void(std::uint8_t level)>> senderLevels_;
  GroupHooks group_;
};

}  // namespace cadence::audiod

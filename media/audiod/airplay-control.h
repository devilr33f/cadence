#pragma once

#include "audiod/airplay-session.h"
#include "audiod/airplay-supervisor.h"
#include "audiod/log.h"
#include "audiod/media-source.h"
#include "dbus/bus.h"
#include "dbus/client.h"
#include "dbus/message.h"

#include <chrono>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

namespace airplay {

// shairport's native d-bus surface (reference §3.1): the two interfaces of one object
inline constexpr std::string_view kShairportPath = "/org/gnome/ShairportSync";
inline constexpr std::string_view kShairportInterface = "org.gnome.ShairportSync";
inline constexpr std::string_view kRemoteInterface = "org.gnome.ShairportSync.RemoteControl";
// the Protocol of the receiver this is for
inline constexpr std::string_view kAirplay2 = "AirPlay 2";
// a receiver that kept answering another Protocol through the connect
inline constexpr std::string_view kWrongProtocol = "wrong_protocol";

// what a remote control PropertiesChanged says about Client, PlayerState and AirplayVolume, the only properties
// read; nullopt when one of them has the wrong type, an unknown PlayerState, or is invalidated
std::optional<Change> remoteChange(const dbus::PropertiesChanged& changed);

}  // namespace airplay

// v1's schedule (reference §2.2, §3.1, §6); tests shorten it
struct AirplayControlTimings {
  // the connect keeps trying for this long after shairport took its name (v1's RECEIVER_WAIT)
  std::chrono::milliseconds connectWait{10000};
  // and tries again this often
  std::chrono::milliseconds connectRetry{20};
  // each call
  std::chrono::milliseconds call = dbus::kDefaultCallTimeout;
  AirplaySession::Timings session{};
};

struct AirplayControlHooks {
  // runs a task on the loop thread: the source's reports go through it with its events handle
  std::function<void(std::function<void()>)> post;
  // the output (AirplaySupervisor's holdOutput, releaseOutput and outputPauses), any thread
  std::function<void()> hold;
  std::function<void(std::uint64_t pausesTaken)> release;
  std::function<std::uint64_t()> pauses;
  // a deliberate slider move, for cadenced (SenderVolume): an implementation hands it to the loop and never blocks
  std::function<void(std::uint8_t level)> senderVolume{};
};

// the receiver's d-bus side (M4d-D5, reference §3.2-3.3), plugged into the supervisor, on its thread, with the
// session it drives. once shairport owns its name it installs the PropertiesChanged match for shairport's unique name
// and object, then reads Protocol (which must be AirPlay 2), the name's owner (which must still be the one attached),
// PlayerState, AirplayVolume and Client, each a call of its own; what changed meanwhile is held and applied after
// them, in order, so nothing is lost. a call that fails or times out, or another Protocol, tries again from Protocol
// until connectWait is up (wrong_protocol, or bus_failed), a property of the wrong type fails at once
// (invalid_state). from then on only PropertiesChanged that shairport's unique name sent about its object count, and
// the session's calls go to that name, one at a time, each within its timeout; the speaker's level as
// SetAirplayVolume.
//
// what the pipe says reaches the session's MediaState while a session is up, and is cleared as it ends. logs carry
// event names, codes, levels and counts: never the client or the metadata
class AirplayControl final : public AirplayReceiver, private AirplaySession::Port {
 public:
  AirplayControl(const Log& log, AirplayControlHooks hooks, AirplayControlTimings timings = {});

  AirplayControl(const AirplayControl&) = delete;
  AirplayControl& operator=(const AirplayControl&) = delete;

  // before the supervisor starts: the source's events handle, which its reports are posted with
  void events(std::shared_ptr<MediaSourceEvents> events);

  // the supervisor's thread, from the source's tasks
  // a pause with the number the output's pause gave it
  void control(MediaAction action, CommandDone done, std::uint64_t pauseNumber = 0);
  void speakerLevel(std::uint8_t level);

  void attached(dbus::Client& client, const std::string& owner) override;
  void signal(const dbus::Message& message) override;
  void detached() override;
  void metadata(const MediaMetadata& metadata) override;
  void progress(const airplay::Progress& progress) override;
  [[nodiscard]] std::optional<Clock::time_point> deadline() const override;
  void tick(Clock::time_point now) override;
  [[nodiscard]] std::string_view failure() const override;

 private:
  // the connect's steps, each answered by the next or a retry
  void readProtocol();
  void readOwner();
  void readState();
  void readVolume();
  void readClient();
  void connected();
  // a property's reply: its value, or nothing when the call failed (retried) or broke (failed)
  const dbus::Value* property(const dbus::Reply& reply);
  void retry();
  void fail(std::string_view code);
  // a call of the connect: its reply reaches step only while this attach lasts
  void ask(dbus::Message message, std::function<void(const dbus::Reply& reply)> step);

  // the session's port
  bool call(const AirplaySession::Call& call) override;
  void hold() override;
  void release(std::uint64_t pausesTaken) override;
  [[nodiscard]] std::uint64_t pausesAsked() const override;
  void playing() override;
  void paused() override;
  void ended() override;
  void request(std::uint8_t level) override;

  void report(std::function<void(MediaSourceEvents&)> report);
  // the metadata the session's MediaState should show now, sent when it differs from what it shows
  void syncMetadata();
  // the latest progress, once, while a session is up
  void syncProgress();

  const Log& log_;
  const AirplayControlHooks hooks_;
  const AirplayControlTimings timings_;
  std::shared_ptr<MediaSourceEvents> events_;
  AirplaySession session_;

  // while attached
  dbus::Client* client_ = nullptr;
  std::string owner_;
  // counts attaches, so a reply of an earlier one is dropped
  std::uint64_t attach_ = 0;
  bool connected_ = false;
  Clock::time_point connectUntil_{};
  std::optional<Clock::time_point> retryAt_;
  bool wrongProtocol_ = false;
  airplay::Snapshot snapshot_;
  // where the pipe last said the track stands, and whether the session has it
  std::optional<airplay::Progress> progress_;
  bool progressShown_ = false;
  std::deque<airplay::Change> held_;
  std::string_view failure_;

  // the pipe's latest, and what the MediaState was last sent
  MediaMetadata metadata_;
  MediaMetadata shown_;
};

}  // namespace cadence::audiod

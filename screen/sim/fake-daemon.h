#pragma once

#include "app/routes/core/rpc.h"
#include "ipc/codec.h"
#include "ipc/seqpacket.h"

#include <atomic>
#include <cstdint>
#include <deque>
#include <functional>
#include <map>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <vector>

namespace cadence::screen::sim {

using Json = cadence::screen::routes::Json;

// one method's scripted answer, as a fixture file or `rpc-answer` sets it (the built-ins are the preview fake
// catalog's, the same shapes and delays)
struct FakeAnswer {
  std::string status = "ok";
  Json json = Json::object();
  int delayMs = 30;
  // a cadence.cards.v1.Page, encoded, sent as the answer's page (devices.panel, tasks.list); empty: none
  std::vector<std::uint8_t> page;
  struct Event {
    int atMs = 0;
    Json json = Json::object();
    bool last = false;
    std::vector<std::uint8_t> page;
  };
  std::vector<Event> events;
  // media.play alone: a MediaPlay envelope goes out for the playable before the answer, as cadenced sends it
  bool withMediaPlay = false;
  // a fixture with a delayMs and no json: the built-in answer, that late (a slow server, to see a skeleton)
  bool delayOnly = false;
};

// parses a fixture file ({"status","json","delayMs","page","events":[{"atMs","json","last","page"}],"withMediaPlay"});
// the json object itself when the file holds a bare one. a page is an array of card fixtures (proto/fixtures/cards/v1
// file names) or "devices" (the fake devices panel, preview/fake-devices.h)
std::optional<FakeAnswer> parseAnswer(const std::string& path, std::string* error);

// cadenced's end of the screen socket, scripted: one thread accepts the screen, answers Hello with the boot
// sequence, serves the catalog's rpcs from the built-in fake data or fixture overrides, and notes what the screen
// sends. macos speaks the stream framing of ipc/seqpacket.cpp, linux the kernel's seqpacket: both behind
// SeqpacketSocket, so this file never branches
class FakeDaemon {
 public:
  struct Options {
    std::string path;
    std::string language = "ru";
    std::string deviceName = "Сим";
  };

  explicit FakeDaemon(Options options);
  ~FakeDaemon();
  FakeDaemon(const FakeDaemon&) = delete;
  FakeDaemon& operator=(const FakeDaemon&) = delete;

  void start();
  void stop();
  // closes the connection, as a cadenced restart would; the screen reconnects with a new Hello
  void dropLink();
  // the language Ready names the screen (before the screen says Hello)
  void setLanguage(const std::string& language);
  [[nodiscard]] std::string language() const;
  // overrides the built-in answer of a method (a loaded fixture, or rpc-answer)
  void setAnswer(const std::string& method, FakeAnswer answer);
  // the calls the screen made of a method: how many, and the last one's args
  [[nodiscard]] std::pair<int, std::string> calls(const std::string& method) const;
  // every *.json in dir, by file name: <method>.json
  bool loadFixtures(const std::string& dir, std::string* error);

  // a json-built envelope the fake sends the screen, as cadenced would (sim/sim.cpp builds them from `send`)
  void sendBuilt(const std::function<void(ipc::OutEnvelope&)>& build);
  // one whole frame as it would go on the wire (send-file: the golden fixtures are such frames)
  void sendRaw(const std::uint8_t* data, std::size_t size);
  // the alarms summary a script sent last (its "alarms_summary" body): alarms.act answers ok and sends it again without
  // the ring it stopped or snoozed, or the timer or reminder it cancelled, one revision higher
  void rememberAlarms(const Json& summary);

  [[nodiscard]] bool helloSeen() const { return hellos_.load() > 0; }
  [[nodiscard]] bool linked() const { return linked_.load(); }
  // the last media_state's, as a token: idle, playing, paused (gated says the gate held it)
  [[nodiscard]] std::string lastMediaState() const;
  // the last transport the screen asked of a cast (NowPlayingCommand): pause, resume, stop, ...; empty: none yet
  [[nodiscard]] std::string lastCastCommand() const;
  [[nodiscard]] std::string lastContextRoute() const;
  [[nodiscard]] const std::string& path() const { return path_; }

 private:
  void run();
  void serve(ipc::SeqpacketSocket conn);
  void handle(const cadence_screen_v1_Envelope& m);
  void boot();
  // cadenced's level as a board without audiod keeps it: the volume keys step it by 5, mute toggles, each one sent
  // back as the level in force (VolumeState)
  void volumeKey(const cadence_screen_v1_InputEvent& e);
  void sendVolume(bool show);
  void answerRpc(const std::string& id, const std::string& method, const Json& args);
  // alarms.act on the remembered summary: false when nothing it names is there
  bool actOnAlarms(const Json& args);
  // runs fn on the fake's own clock, ms from now
  void later(int ms, std::function<void()> fn);
  void sendEnvelope(const cadence_screen_v1_Envelope& m);
  FakeAnswer builtin(const std::string& method, const Json& args);

  Options options_;
  std::string path_;
  int listen_ = -1;
  // the connection is the fake thread's (it alone recvs); sends from any thread take sendMu_
  ipc::SeqpacketSocket conn_;
  std::thread thread_;
  std::atomic<bool> stopping_{false};
  std::atomic<bool> linked_{false};
  std::atomic<int> hellos_{0};
  mutable std::mutex mu_;
  std::mutex sendMu_;
  std::map<std::string, FakeAnswer> answers_;
  std::map<std::string, std::pair<int, std::string>> calls_;
  int level_ = 30;
  bool muted_ = false;
  std::map<std::string, std::uint32_t> seqs_;
  std::string lastMediaState_;
  std::string lastCastCommand_;
  std::string lastContextRoute_;
  // the alarms summary alarms.act edits (relative times made absolute), null before a script sent one
  Json alarms_;
  // the timer queue and its callbacks, the fake's thread alone
  struct Due {
    std::int64_t atMs;
    std::function<void()> fn;
  };
  std::vector<Due> due_;
  std::uint64_t plays_ = 0;
};

}  // namespace cadence::screen::sim

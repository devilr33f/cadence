// fake-soloist: a stand-in for spotify's soloist in tests (rule 7: no test ever runs the real one or casts). it takes
// soloist's command line: --version prints a build line; anything else writes <data-dir>/fake-argv (its arguments, a
// line each, then --env-- and its environment), prints soloist-like lines (a stamped login failed when the key is
// "bad"), writes the port of its websocket to <data-dir>/ws.port and serves it.
//
// the device name picks a start: exit-<n> exits with n at once, silent writes no port, mute writes the port of a
// server that never answers the handshake, deaf serves one that stops reading after it; any other name serves
// normally. "Cadence E2E" plays make e2e's part on its own: once the handshake went out it is logged in and active and
// plays a track with its tone, then sends the app's stored volume and a second later a slider move; it pauses and plays
// as told, and appends each command it receives to <data-dir>/commands.log before it answers it.
//
// built as fake-soloist-e2e (CADENCE_FAKE_SOLOIST_FRESH) it is the build the e2e's fake cdn serves: --version names a
// build of yesterday, whatever the day, so the updater takes it.
//
// the test drives it through <data-dir>/fake-inbox (removed as it starts), a line at a time: a line starting with {
// goes out on the websocket as it is; !exit <n>, !hangup (closes the websocket and serves a new one, which reads, on a
// new port), !tone on|off (a 1 kHz tone through the libpulse shim), !print <text> (a line on stdout), !eprint <text>
// (one on stderr), !error-next (the next control command is answered with an error instead of its command_result) and
// !echo <offset>|off (a set_volume, after its command_result, comes back as volume_changed of the level plus offset, by
// default 0). each text frame it receives is appended to <data-dir>/fake-received; the bytes of tone the shim took so
// far are in <data-dir>/fake-tone
#include "pulse-shim/abi.h"
#include "support/ws-server.h"

#include <unistd.h>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <filesystem>
#include <format>
#include <fstream>
#include <memory>
#include <numbers>
#include <optional>
#include <sstream>
#include <string>
#include <string_view>
#include <thread>
#include <utility>
#include <vector>

extern char** environ;

namespace {

using cadence::test_support::WsHandshake;
using cadence::test_support::WsServer;
using cadence::test_support::WsServerOptions;
using namespace std::chrono_literals;

constexpr std::string_view kVersionLine = "soloist 1.3.10.18 build 1793059327 (20261027) (gfake) (linux/test)";
constexpr std::string_view kStamp = "2026-10-19 08:18:40.642: ";
constexpr std::string_view kE2eName = "Cadence E2E";
constexpr std::string_view kE2eUp =
    R"({"type":"auth_state","logged_in":true,"is_active":true,"device_name":"Cadence E2E"})";
constexpr std::string_view kE2eTrack =
    R"({"type":"playback_state","status":"playing","position":{"position_ms":0},"item":{"uri":"spotify:track:fake",)"
    R"("decorations":{"identity":{"name":"Cadence Fake Track"},"creators":[{"entity":{"decorations":{"identity":)"
    R"({"name":"Cadence Fake Artist"}}}}],"playback":{"duration_ms":180000}}}})";
// the level the app kept, which rule 7 drops, then a deliberate slider move
constexpr std::string_view kE2eStored = R"({"type":"volume_changed","volume":55})";
constexpr std::string_view kE2eMoved = R"({"type":"volume_changed","volume":25})";
// in the jail: the shim's own default socket serves it
constexpr std::string_view kJailHome = "/home/soloist";
constexpr std::uint32_t kRate = 44100;
constexpr std::size_t kFrameBytes = 4;

struct Args {
  std::string deviceName;
  std::string apiKey;
  std::string dataDir;
};

Args parse(int argc, char** argv) {
  Args args;
  for (int i = 1; i + 1 < argc; ++i) {
    const std::string_view arg = argv[i];
    if (arg == "--device-name")
      args.deviceName = argv[++i];
    else if (arg == "--api-key")
      args.apiKey = argv[++i];
    else if (arg == "--data-dir")
      args.dataDir = argv[++i];
  }
  return args;
}

void line(std::string_view text) {
  std::fwrite(text.data(), 1, text.size(), stdout);
  std::fputc('\n', stdout);
  std::fflush(stdout);
}

#ifdef CADENCE_FAKE_SOLOIST_FRESH
std::string versionLine() {
  using namespace std::chrono;
  const auto yesterday = floor<days>(system_clock::now()) - days(1);
  return std::format("soloist 1.3.10.18 build {} ({:%Y%m%d}) (gfake) (linux/test)",
                     duration_cast<seconds>(yesterday.time_since_epoch()).count(), yesterday);
}
#else
std::string versionLine() { return std::string(kVersionLine); }
#endif

[[noreturn]] void quit(int code) {
  std::fflush(stdout);
  std::_Exit(code);
}

void writeFile(const std::string& path, const std::string& text) {
  const auto next = path + ".next";
  std::ofstream(next, std::ios::trunc) << text;
  std::filesystem::rename(next, path);
}

void append(const std::string& path, std::string_view text) {
  std::ofstream(path, std::ios::app) << text << '\n';
}

// "key":"value" or "key":number in a command the client sent
std::string field(std::string_view text, std::string_view key) {
  const auto quoted = "\"" + std::string(key) + "\":";
  const auto at = text.find(quoted);
  if (at == std::string_view::npos)
    return {};
  auto rest = text.substr(at + quoted.size());
  if (rest.starts_with('"')) {
    rest.remove_prefix(1);
    return std::string(rest.substr(0, rest.find('"')));
  }
  return std::string(rest.substr(0, rest.find_first_of(",}")));
}

// a 1 kHz tone at half scale, s16 stereo at 44.1 kHz, through the shim while on
class Tone {
 public:
  explicit Tone(std::string server) : server_(std::move(server)) {}
  ~Tone() {
    stopping_ = true;
    if (thread_.joinable())
      thread_.join();
  }

  void set(bool on) {
    on_ = on;
    if (on && !thread_.joinable())
      thread_ = std::thread([this] { play(); });
  }
  // the bytes the shim took, or -1 once the stream failed
  [[nodiscard]] std::int64_t written() const { return written_; }

 private:
  void play() {
    auto* m = pa_threaded_mainloop_new();
    if (m == nullptr || pa_threaded_mainloop_start(m) != 0) {
      written_ = -1;
      return;
    }
    pa_threaded_mainloop_lock(m);
    auto* c = pa_context_new(pa_threaded_mainloop_get_api(m), "fake-soloist");
    const char* server = server_.empty() ? nullptr : server_.c_str();
    pa_stream* s = nullptr;
    if (c != nullptr && pa_context_connect(c, server, PA_CONTEXT_NOFLAGS, nullptr) == 0) {
      const pa_sample_spec spec{PA_SAMPLE_S16LE, kRate, 2};
      s = pa_stream_new(c, "tone", &spec, nullptr);
      if (s != nullptr && pa_stream_connect_playback(s, nullptr, nullptr, PA_STREAM_NOFLAGS, nullptr, nullptr) != 0)
        s = nullptr;
    }
    pa_threaded_mainloop_unlock(m);
    if (s == nullptr) {
      written_ = -1;
      return;
    }
    std::vector<std::int16_t> samples;
    std::size_t phase = 0;
    while (!stopping_) {
      pa_threaded_mainloop_lock(m);
      const auto state = pa_stream_get_state(s);
      const auto room = state == PA_STREAM_READY ? pa_stream_writable_size(s) : 0;
      if (state == PA_STREAM_FAILED || room == SIZE_MAX) {
        pa_threaded_mainloop_unlock(m);
        written_ = -1;
        return;
      }
      const auto frames = on_ ? std::min<std::size_t>(room / kFrameBytes, kRate / 50) : 0;
      if (frames > 0) {
        samples.clear();
        for (std::size_t i = 0; i < frames; ++i, ++phase) {
          const auto value = static_cast<std::int16_t>(
              std::lround(16384 * std::sin(2 * std::numbers::pi * 1000 * static_cast<double>(phase) / kRate)));
          samples.push_back(value);
          samples.push_back(value);
        }
        if (pa_stream_write(s, samples.data(), frames * kFrameBytes, nullptr, 0, PA_SEEK_RELATIVE) == 0)
          written_ += static_cast<std::int64_t>(frames * kFrameBytes);
      }
      pa_threaded_mainloop_unlock(m);
      if (frames == 0)
        std::this_thread::sleep_for(5ms);
    }
  }

  std::string server_;
  std::atomic<bool> on_{false};
  std::atomic<bool> stopping_{false};
  std::atomic<std::int64_t> written_{0};
  std::thread thread_;
};

class Fake {
 public:
  Fake(Args args, WsHandshake handshake, bool reading)
      : args_(std::move(args)),
        handshake_(handshake),
        reading_(reading),
        e2e_(args_.deviceName == kE2eName),
        tone_(args_.dataDir == kJailHome
                  ? std::string()
                  : "unix:" + std::filesystem::path(args_.dataDir).parent_path().string() + "/run/pcm.sock") {}

  [[noreturn]] void run() {
    serve();
    for (;;) {
      inbox();
      e2e();
      received();
      if (const auto written = tone_.written(); written != toneReported_) {
        toneReported_ = written;
        writeFile(path("fake-tone"), std::to_string(written));
      }
      std::this_thread::sleep_for(5ms);
    }
  }

 private:
  [[nodiscard]] std::string path(std::string_view name) const { return args_.dataDir + "/" + std::string(name); }

  void serve() {
    server_ = std::make_unique<WsServer>(WsServerOptions{.handshake = handshake_, .reading = reading_});
    seen_ = 0;
    writeFile(path("ws.port"), std::to_string(server_->port()) + "\n");
  }

  void inbox() {
    std::ifstream in(path("fake-inbox"), std::ios::binary);
    if (!in)
      return;
    in.seekg(static_cast<std::streamoff>(read_));
    std::string text((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
    for (auto end = text.find('\n'); end != std::string::npos; end = text.find('\n')) {
      const auto command = text.substr(0, end);
      read_ += end + 1;
      text.erase(0, end + 1);
      handle(command);
    }
  }

  void handle(const std::string& command) {
    if (command.starts_with('{')) {
      server_->sendText(command);
    } else if (command.starts_with("!exit ")) {
      quit(std::atoi(command.c_str() + 6));
    } else if (command == "!hangup") {
      server_->hangUp();
      reading_ = true;
      serve();
    } else if (command == "!tone on" || command == "!tone off") {
      tone_.set(command == "!tone on");
    } else if (command.starts_with("!print ")) {
      line(std::string_view(command).substr(7));
    } else if (command.starts_with("!eprint ")) {
      const auto text = std::string_view(command).substr(8);
      std::fwrite(text.data(), 1, text.size(), stderr);
      std::fputc('\n', stderr);
      std::fflush(stderr);
    } else if (command == "!error-next") {
      errorNext_ = true;
    } else if (command == "!echo off") {
      echo_.reset();
    } else if (command.starts_with("!echo ")) {
      echo_ = std::atoi(command.c_str() + 6);
    }
  }

  // the e2e's part, by the time since the handshake went out
  void e2e() {
    using Clock = std::chrono::steady_clock;
    if (!e2e_ || e2eStep_ == 3)
      return;
    if (e2eStep_ == 0) {
      if (!server_->waitHandshake(0ms))
        return;
      e2eBegan_ = Clock::now();
      server_->sendText(kE2eUp);
      server_->sendText(kE2eTrack);
      tone_.set(true);
      e2eStep_ = 1;
    }
    const auto since = Clock::now() - e2eBegan_;
    if (e2eStep_ == 1 && since >= 1s) {
      server_->sendText(kE2eStored);
      e2eStep_ = 2;
    }
    if (e2eStep_ == 2 && since >= 2s) {
      server_->sendText(kE2eMoved);
      e2eStep_ = 3;
    }
  }

  void received() {
    const auto frames = server_->frames();
    for (; seen_ < frames.size(); ++seen_) {
      const auto& frame = frames[seen_];
      if (frame.opcode != 1)
        continue;
      append(path("fake-received"), frame.payload);
      // before the answer: whoever sees the answer finds the command logged
      if (e2e_)
        append(path("commands.log"), frame.payload);
      const auto name = field(frame.payload, "command");
      if (name == "set_volume") {
        server_->sendText(R"({"type":"command_result","command":"set_volume"})");
        if (echo_) {
          const auto level = std::clamp(std::atoi(field(frame.payload, "volume").c_str()) + *echo_, 0, 100);
          server_->sendText(R"({"type":"volume_changed","volume":)" + std::to_string(level) + "}");
        }
      } else if (std::exchange(errorNext_, false)) {
        server_->sendText(R"({"type":"error","message":"command requires authentication"})");
      } else {
        server_->sendText(R"({"type":"command_result","command":")" + name + "\"}");
        if (e2e_ && (name == "pause" || name == "play")) {
          tone_.set(name == "play");
          server_->sendText(R"({"type":"playback_changed","status":")" +
                            std::string(name == "play" ? "playing" : "paused") + "\"}");
        }
      }
    }
  }

  Args args_;
  WsHandshake handshake_;
  bool reading_ = true;
  bool e2e_ = false;
  int e2eStep_ = 0;
  std::chrono::steady_clock::time_point e2eBegan_;
  Tone tone_;
  std::unique_ptr<WsServer> server_;
  std::size_t seen_ = 0;
  std::size_t read_ = 0;
  std::int64_t toneReported_ = -2;
  bool errorNext_ = false;
  std::optional<int> echo_ = 0;
};

}  // namespace

int main(int argc, char** argv) {
  if (argc == 2 && std::string_view(argv[1]) == "--version") {
    line(versionLine());
    return 0;
  }
  auto args = parse(argc, argv);
  if (args.dataDir.empty())
    return 2;
  // what the test told the last run is not for this one
  std::error_code ignored;
  std::filesystem::remove(args.dataDir + "/fake-inbox", ignored);
  std::ostringstream record;
  for (int i = 1; i < argc; ++i)
    record << argv[i] << '\n';
  record << "--env--\n";
  for (char** variable = environ; *variable != nullptr; ++variable)
    record << *variable << '\n';
  writeFile(args.dataDir + "/fake-argv", record.str());

  line(std::string(kStamp) + "client expires in 86 days");
  if (args.apiKey == "bad")
    line("2026-10-19 08:18:40.695: login failed: make sure --api-key is valid");
  line(std::string(kStamp) + "running");

  const std::string_view name = args.deviceName;
  if (name.starts_with("exit-"))
    quit(std::atoi(std::string(name.substr(5)).c_str()));
  if (name == "silent")
    for (;;)
      std::this_thread::sleep_for(1s);
  Fake fake(std::move(args), name == "mute" ? WsHandshake::Silent : WsHandshake::Accept, name != "deaf");
  fake.run();
}

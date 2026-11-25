// fake-shairport: the e2e's stand-in for shairport sync (M4d-D8; rule 7: no test casts or runs the real receiver),
// which audiod runs as it runs shairport: through the runtime's loader, with --configfile <scratch>/shairport.conf and
// the private bus in DBUS_SYSTEM_BUS_ADDRESS. unlike the tests' scripted stand-in in their own process
// (fake-shairport.h), it plays one fixed cast on its own.
//
// it takes the name and both fifos from its config, exports /org/gnome/ShairportSync as shairport's native interface
// does, org.gnome.ShairportSync (Protocol "AirPlay 2", DropSession) and its RemoteControl (PlayerState, AirplayVolume,
// Client; Play, Pause, Next, Previous, SetAirplayVolume), and owns org.gnome.ShairportSync. once the receiver read its
// Client, the last read of its connect, the cast starts: a Client, then PlayerState Playing, a 48 kHz s16 stereo tone
// (1500 Hz left, 1000 Hz right, -6 dBFS) into the audio fifo in real time, and the track (a play stream's begin, then
// its title and artist) into the metadata fifo; 1.5 s later the volume the sender's app stored (-15 dB, 50), which the
// receiver must drop, and 1.5 s after that a slider move (-9 dB, 70), which it must pass on. Pause makes it Paused and
// stops the tone, Play makes it Playing and the tone goes on, DropSession ends the cast (Not Available, the play
// stream's end). each call, its method and arguments, is appended to <scratch>/fake-shairport.log. -V prints a version
// line
#include "dbus/bus.h"
#include "dbus/client.h"

#include <fcntl.h>
#include <poll.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <cerrno>
#include <chrono>
#include <cmath>
#include <csignal>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <iterator>
#include <numbers>
#include <optional>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace {

using Clock = std::chrono::steady_clock;
using namespace std::chrono_literals;
using cadence::dbus::Value;

constexpr std::string_view kVersion = "fake-shairport (the cadence e2e's stand-in for shairport sync)";
constexpr std::string_view kName = "org.gnome.ShairportSync";
constexpr std::string_view kPath = "/org/gnome/ShairportSync";
constexpr std::string_view kMain = "org.gnome.ShairportSync";
constexpr std::string_view kRemote = "org.gnome.ShairportSync.RemoteControl";

constexpr std::string_view kClient = "cadence-fake-airplay-client";
constexpr std::string_view kTitle = "Cadence Fake AirPlay Track";
constexpr std::string_view kArtist = "Cadence Fake AirPlay Artist";
// mute until a sender says otherwise; then the level its app stored, then its slider's move
constexpr double kQuiet = -144.0;
constexpr double kStored = -15.0;
constexpr double kMoved = -9.0;
// after the receiver's connect: the cast, then each volume
constexpr auto kCastAfter = 200ms;
constexpr auto kStoredAfter = 1700ms;
constexpr auto kMovedAfter = 3200ms;

constexpr double kRate = 48000;
constexpr double kAmplitude = 0.5;
constexpr double kLeftHz = 1500;
constexpr double kRightHz = 1000;
// 10 ms of 48 kHz frames per write
constexpr std::size_t kChunkFrames = 480;
constexpr auto kChunk = 10ms;

constexpr int kExitUsage = 2;
constexpr int kExitConfig = 3;
constexpr int kExitBus = 4;
constexpr int kExitName = 5;

// a value of section in the libconfig file audiod writes (`section =\n{\n  key = "value";`), its \ escapes and the
// $${ that stands for ${ undone
std::optional<std::string> setting(std::string_view text, std::string_view section, std::string_view key) {
  const auto head = std::string(section) + " =\n{\n";
  auto at = text.find(head);
  while (at != std::string_view::npos && at > 0 && text[at - 1] != '\n')
    at = text.find(head, at + 1);
  if (at == std::string_view::npos)
    return std::nullopt;
  const auto body = text.substr(at + head.size(), text.find("\n};", at) - at - head.size());
  const auto prefix = "  " + std::string(key) + " = \"";
  for (std::size_t line = 0; line < body.size();) {
    const auto end = std::min(body.find('\n', line), body.size());
    const auto row = body.substr(line, end - line);
    line = end + 1;
    if (!row.starts_with(prefix))
      continue;
    std::string value;
    for (std::size_t i = prefix.size(); i < row.size(); ++i) {
      if (row[i] == '"')
        return row.substr(i) == "\";" ? std::optional(value) : std::nullopt;
      if (row[i] == '\\' && i + 1 < row.size())
        ++i;
      else if (row.substr(i, 3) == "$${")
        ++i;
      value += row[i];
    }
    return std::nullopt;
  }
  return std::nullopt;
}

std::string hex(std::string_view code) {
  std::string out;
  for (const char c : code) {
    std::array<char, 3> digits{};
    std::snprintf(digits.data(), digits.size(), "%02x", static_cast<unsigned char>(c));
    out += digits.data();
  }
  return out;
}

std::string base64(std::string_view data) {
  constexpr std::string_view kAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  std::string out;
  for (std::size_t at = 0; at < data.size(); at += 3) {
    std::uint32_t group = 0;
    const auto take = std::min<std::size_t>(3, data.size() - at);
    for (std::size_t i = 0; i < 3; ++i)
      group = (group << 8) | (i < take ? static_cast<unsigned char>(data[at + i]) : 0U);
    for (std::size_t i = 0; i < 4; ++i)
      out += i <= take ? kAlphabet[(group >> (18 - 6 * i)) & 0x3fU] : '=';
  }
  return out;
}

// an item of shairport's metadata pipe as its documentation shows one
std::string item(std::string_view type, std::string_view code, std::string_view data = {}) {
  auto text = "<item><type>" + hex(type) + "</type><code>" + hex(code) + "</code><length>" +
              std::to_string(data.size()) + "</length>";
  if (data.empty())
    return text + "</item>\n";
  return text + "\n<data encoding=\"base64\">\n" + base64(data) + "</data></item>\n";
}

class Fd {
 public:
  Fd() = default;
  explicit Fd(int fd) : fd_(fd) {}
  ~Fd() { reset(); }
  Fd(const Fd&) = delete;
  Fd& operator=(const Fd&) = delete;
  Fd(Fd&& other) noexcept : fd_(std::exchange(other.fd_, -1)) {}
  Fd& operator=(Fd&& other) noexcept {
    if (this != &other) {
      reset();
      fd_ = std::exchange(other.fd_, -1);
    }
    return *this;
  }
  [[nodiscard]] int get() const { return fd_; }
  [[nodiscard]] bool valid() const { return fd_ >= 0; }
  void reset() {
    if (fd_ >= 0)
      ::close(fd_);
    fd_ = -1;
  }

 private:
  int fd_ = -1;
};

// a fifo opened for writing without blocking, as shairport opens it once audiod reads it; again after a reader went
class Fifo {
 public:
  explicit Fifo(std::string path) : path_(std::move(path)) {}

  // all of it, or nothing once the reader is gone or the fifo is full: a real-time writer drops what finds no room
  void write(std::string_view bytes) {
    if (!fd_.valid())
      fd_ = Fd(::open(path_.c_str(), O_WRONLY | O_NONBLOCK | O_CLOEXEC));
    while (fd_.valid() && !bytes.empty()) {
      const auto sent = ::write(fd_.get(), bytes.data(), bytes.size());
      if (sent > 0) {
        bytes.remove_prefix(static_cast<std::size_t>(sent));
        continue;
      }
      if (sent < 0 && errno == EINTR)
        continue;
      if (sent < 0 && errno == EPIPE)
        fd_.reset();
      return;
    }
  }

 private:
  std::string path_;
  Fd fd_;
};

class Cast {
 public:
  Cast(cadence::dbus::Client& client, std::string audio, std::string metadata, std::string log)
      : client_(client), audio_(std::move(audio)), metadata_(std::move(metadata)), log_(std::move(log)) {}

  void exportObjects() {
    cadence::dbus::Interface main{.name = std::string(kMain), .methods = {}, .properties = {}};
    main.properties["Protocol"] = {.get = [] { return Value::string("AirPlay 2"); }, .set = {}};
    main.methods["DropSession"] = [this](const cadence::dbus::Message&) {
      record("DropSession");
      end();
      return cadence::dbus::MethodResult{};
    };
    client_.exportInterface(std::string(kPath), std::move(main));

    cadence::dbus::Interface remote{.name = std::string(kRemote), .methods = {}, .properties = {}};
    remote.properties["PlayerState"] = {.get = [this] { return Value::string(state_); }, .set = {}};
    remote.properties["AirplayVolume"] = {.get = [this] { return Value::float64(volume_); }, .set = {}};
    remote.properties["Client"] = {.get =
                                       [this] {
                                         if (!castAt_ && steps_ == 0)
                                           castAt_ = Clock::now();
                                         return Value::string(sender_);
                                       },
                                   .set = {}};
    remote.methods["Play"] = [this](const cadence::dbus::Message&) {
      record("Play");
      if (steps_ > 0 && state_ != "Not Available")
        play();
      return cadence::dbus::MethodResult{};
    };
    remote.methods["Pause"] = [this](const cadence::dbus::Message&) {
      record("Pause");
      if (state_ == "Playing")
        setState("Paused");
      return cadence::dbus::MethodResult{};
    };
    remote.methods["Next"] = [this](const cadence::dbus::Message&) {
      record("Next");
      return cadence::dbus::MethodResult{};
    };
    remote.methods["Previous"] = [this](const cadence::dbus::Message&) {
      record("Previous");
      return cadence::dbus::MethodResult{};
    };
    remote.methods["SetAirplayVolume"] = [this](const cadence::dbus::Message& call) {
      const auto db = call.body.size() == 1 ? call.body.front().asDouble() : std::nullopt;
      std::array<char, 64> line{};
      std::snprintf(line.data(), line.size(), "SetAirplayVolume %.2f", db.value_or(-999.0));
      record(line.data());
      return cadence::dbus::MethodResult{};
    };
    client_.exportInterface(std::string(kPath), std::move(remote));
  }

  // what is due by now; when it next has something to do
  std::optional<Clock::time_point> run(Clock::time_point now) {
    step(now);
    if (state_ != "Playing")
      return nextStep();
    // a writer held up for long starts again from now rather than catching up in a burst
    if (now - nextChunk_ > 200ms)
      nextChunk_ = now;
    while (nextChunk_ <= now) {
      tone();
      nextChunk_ += kChunk;
    }
    const auto next = nextStep();
    return next ? std::min(*next, nextChunk_) : nextChunk_;
  }

 private:
  void step(Clock::time_point now) {
    if (!castAt_)
      return;
    const auto at = *castAt_;
    if (steps_ == 0 && now >= at + kCastAfter) {
      ++steps_;
      sender_ = kClient;
      client_.emitPropertiesChanged(std::string(kPath), std::string(kRemote), {{"Client", Value::string(sender_)}});
      play();
      metadata_.write(item("ssnc", "pbeg") + item("ssnc", "mdst") + item("core", "minm", kTitle) +
                      item("core", "asar", kArtist) + item("ssnc", "mden"));
    }
    if (steps_ == 1 && now >= at + kStoredAfter) {
      ++steps_;
      setVolume(kStored);
    }
    if (steps_ == 2 && now >= at + kMovedAfter) {
      ++steps_;
      setVolume(kMoved);
    }
  }

  [[nodiscard]] std::optional<Clock::time_point> nextStep() const {
    if (!castAt_)
      return std::nullopt;
    switch (steps_) {
      case 0:
        return *castAt_ + kCastAfter;
      case 1:
        return *castAt_ + kStoredAfter;
      case 2:
        return *castAt_ + kMovedAfter;
      default:
        return std::nullopt;
    }
  }

  void play() {
    nextChunk_ = Clock::now();
    setState("Playing");
  }

  void end() {
    castAt_.reset();
    steps_ = 3;
    setState("Not Available");
    sender_.clear();
    client_.emitPropertiesChanged(std::string(kPath), std::string(kRemote), {{"Client", Value::string(sender_)}});
    metadata_.write(item("ssnc", "pend"));
  }

  void setState(std::string state) {
    if (state == state_)
      return;
    state_ = std::move(state);
    client_.emitPropertiesChanged(std::string(kPath), std::string(kRemote), {{"PlayerState", Value::string(state_)}});
  }

  void setVolume(double db) {
    volume_ = db;
    client_.emitPropertiesChanged(std::string(kPath), std::string(kRemote), {{"AirplayVolume", Value::float64(db)}});
  }

  void tone() {
    std::string bytes(kChunkFrames * 4, '\0');
    for (std::size_t i = 0; i < kChunkFrames; ++i, ++frame_) {
      const auto t = static_cast<double>(frame_) / kRate;
      const auto sample = [t](double hz) {
        return static_cast<std::int16_t>(std::lround(kAmplitude * 32767.0 * std::sin(2.0 * std::numbers::pi * hz * t)));
      };
      const std::array<std::int16_t, 2> pair{sample(kLeftHz), sample(kRightHz)};
      for (std::size_t channel = 0; channel < 2; ++channel) {
        const auto value = static_cast<std::uint16_t>(pair[channel]);
        bytes[i * 4 + channel * 2] = static_cast<char>(value & 0xffU);
        bytes[i * 4 + channel * 2 + 1] = static_cast<char>(value >> 8);
      }
    }
    audio_.write(bytes);
  }

  void record(std::string_view call) const {
    std::ofstream(log_, std::ios::app) << call << '\n';
  }

  cadence::dbus::Client& client_;
  Fifo audio_;
  Fifo metadata_;
  std::string log_;
  std::string state_ = "Not Available";
  double volume_ = kQuiet;
  std::string sender_;
  // once the receiver connected, and how far the cast got
  std::optional<Clock::time_point> castAt_;
  int steps_ = 0;
  Clock::time_point nextChunk_{};
  std::uint64_t frame_ = 0;
};

std::string dirOf(const std::string& path) {
  const auto slash = path.rfind('/');
  return slash == std::string::npos ? std::string(".") : path.substr(0, slash);
}

}  // namespace

int main(int argc, char** argv) {
  const std::vector<std::string> args(argv + 1, argv + argc);
  if (args.size() == 1 && args.front() == "-V") {
    std::printf("%.*s\n", static_cast<int>(kVersion.size()), kVersion.data());
    return 0;
  }
  if (args.size() != 2 || args.front() != "--configfile")
    return kExitUsage;
  // a reader that went must not kill it: the write fails, and the fifo is opened again
  std::signal(SIGPIPE, SIG_IGN);

  std::ifstream in(args[1]);
  const std::string text{std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
  const auto name = setting(text, "general", "name");
  const auto audio = setting(text, "pipe", "name");
  const auto metadata = setting(text, "metadata", "pipe_name");
  if (!name || name->empty() || !audio || !metadata)
    return kExitConfig;

  const char* address = std::getenv("DBUS_SYSTEM_BUS_ADDRESS");
  if (address == nullptr)
    return kExitBus;
  auto connected = cadence::dbus::Client::connect(address);
  if (!connected.ok())
    return kExitBus;
  auto& client = connected.value();
  Cast cast(client, *audio, *metadata, dirOf(args[1]) + "/fake-shairport.log");
  cast.exportObjects();
  const auto owned =
      client.callAndWait(cadence::dbus::requestNameCall(std::string(kName), cadence::dbus::kNameDoNotQueue));
  if (owned.status != cadence::dbus::CallStatus::Ok || owned.body.size() != 1 ||
      owned.body.front().asUint32() != cadence::dbus::kNamePrimaryOwner)
    return kExitName;

  while (client.open()) {
    const auto now = Clock::now();
    auto wake = cast.run(now);
    if (const auto deadline = client.nextDeadline(); deadline && (!wake || *deadline < *wake))
      wake = deadline;
    int timeout = 1000;
    if (client.hasBuffered())
      timeout = 0;
    else if (wake)
      timeout = static_cast<int>(
          std::clamp<long long>(std::chrono::ceil<std::chrono::milliseconds>(*wake - now).count(), 0, 1000));
    pollfd readable{client.fd(), POLLIN, 0};
    ::poll(&readable, 1, timeout);
    client.process();
  }
  return 0;
}

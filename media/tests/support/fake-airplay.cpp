// a stand-in for avahi-daemon, nqptp and shairport-sync, whichever it runs as, for the airplay supervisor's tests: no
// test ever runs the real ones. the supervisor starts it through the runtime's loader as <dir>/runtime/bin/<name>; it
// finds the test's rig at <dir>/fake, writes how it was started to <rig>/<name>.start, adds "<name> <pid>" to
// <rig>/starts, and does what <rig>/<name>.mode says:
//   ready (the default): avahi and shairport own their names on DBUS_SYSTEM_BUS_ADDRESS; nqptp binds the control port
//     <rig>/timing names and makes its interface at the path there once "/nqptp T" came
//   exit: exits at once with 3
//   silent: never gets ready
//   exit-after=<ms>: ready, then exits with 4 after ms
//   release-after=<ms>: ready, then lets its name go after ms and stays
//   linger-term=<ms>: ready; a SIGTERM adds "<name> term" to <rig>/events, and ms later "<name> exit" as it exits
//     with 0, as a daemon that says goodbye first would
//   hold-term: ready; a SIGTERM adds "<name> term" to <rig>/events, and nothing else happens
#include "dbus/bus.h"
#include "dbus/client.h"

#include <arpa/inet.h>
#include <dirent.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <poll.h>
#include <signal.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <chrono>
#include <csignal>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <functional>
#include <iterator>
#include <optional>
#include <sstream>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

extern char** environ;

namespace {

using Clock = std::chrono::steady_clock;

std::string dirOf(const std::string& path) {
  const auto slash = path.rfind('/');
  return slash == std::string::npos ? std::string(".") : path.substr(0, slash);
}

std::string read(const std::string& path) {
  std::ifstream in(path);
  return {std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
}

std::string firstLine(const std::string& path) {
  std::ifstream in(path);
  std::string line;
  std::getline(in, line);
  return line;
}

std::string statusField(const std::string& name) {
  std::ifstream in("/proc/self/status");
  for (std::string line; std::getline(in, line);)
    if (line.starts_with(name + ":")) {
      auto value = line.substr(name.size() + 1);
      value.erase(0, value.find_first_not_of(" \t"));
      return value;
    }
  return {};
}

std::string link(const std::string& path) {
  char target[512]{};
  const auto size = ::readlink(path.c_str(), target, sizeof target - 1);
  return size > 0 ? std::string(target, static_cast<std::size_t>(size)) : std::string();
}

std::string openFds() {
  std::string fds;
  DIR* dir = ::opendir("/proc/self/fd");
  if (dir == nullptr)
    return fds;
  const int own = ::dirfd(dir);
  while (const dirent* entry = ::readdir(dir)) {
    const std::string_view name = entry->d_name;
    if (name == "." || name == ".." || std::atoi(entry->d_name) == own)
      continue;
    fds += (fds.empty() ? "" : ",") + std::string(name);
  }
  ::closedir(dir);
  return fds;
}

// every pid an earlier start recorded is gone: none still runs, none is a zombie waiting for its reap
bool earlierGone(const std::string& rig) {
  std::ifstream in(rig + "/starts");
  for (std::string name; in >> name;) {
    long pid = 0;
    in >> pid;
    if (pid == ::getpid())
      continue;
    if (::access(("/proc/" + std::to_string(pid)).c_str(), F_OK) == 0)
      return false;
  }
  return true;
}

void record(const std::string& rig, const std::string& name, const std::vector<std::string>& args,
            bool timingAtStart) {
  std::ostringstream out;
  int deathSignal = 0;
  ::prctl(PR_GET_PDEATHSIG, &deathSignal, 0UL, 0UL, 0UL);
  out << "pid=" << ::getpid() << "\nppid=" << ::getppid() << "\nnice=" << ::getpriority(PRIO_PROCESS, 0)
      << "\noom=" << firstLine("/proc/self/oom_score_adj") << "\npdeathsig=" << deathSignal
      << "\nnonewprivs=" << statusField("NoNewPrivs") << "\nsigblk=" << statusField("SigBlk")
      << "\nsigign=" << statusField("SigIgn") << "\nfds=" << openFds() << "\nstdin=" << link("/proc/self/fd/0")
      << "\nstdout=" << link("/proc/self/fd/1") << "\nstderr=" << link("/proc/self/fd/2")
      << "\nclean=" << (earlierGone(rig) ? 1 : 0) << "\ntiming_at_start=" << (timingAtStart ? 1 : 0) << '\n';
  for (const auto& arg : args)
    out << "arg=" << arg << '\n';
  for (char** variable = environ; *variable != nullptr; ++variable)
    out << "env=" << *variable << '\n';
  const auto path = rig + "/" + name + ".start";
  std::ofstream(path + ".next") << out.str();
  std::rename((path + ".next").c_str(), path.c_str());
  std::ofstream(rig + "/starts", std::ios::app) << name << ' ' << ::getpid() << '\n';
}

struct Mode {
  std::string kind = "ready";
  std::chrono::milliseconds after{};
  std::optional<Clock::time_point> at;
};

Mode modeOf(const std::string& rig, const std::string& name) {
  Mode mode;
  const auto text = firstLine(rig + "/" + name + ".mode");
  if (text.empty())
    return mode;
  const auto equals = text.find('=');
  mode.kind = text.substr(0, equals);
  if (equals != std::string::npos) {
    mode.after = std::chrono::milliseconds(std::atol(text.c_str() + equals + 1));
    mode.at = Clock::now() + mode.after;
  }
  return mode;
}

// what a SIGTERM does in the term modes, all made before its handler is in: the handler makes async-signal-safe calls
// alone
int eventsFd = -1;
std::string termLine;
std::string exitLine;
bool lingers = false;
timespec lingerFor{};

extern "C" void onTerm(int /*signo*/) {
  [[maybe_unused]] auto written = ::write(eventsFd, termLine.data(), termLine.size());
  if (!lingers)
    return;
  ::nanosleep(&lingerFor, nullptr);
  written = ::write(eventsFd, exitLine.data(), exitLine.size());
  ::_exit(0);
}

void trapTerm(const std::string& rig, const std::string& name, const Mode& mode) {
  eventsFd = ::open((rig + "/events").c_str(), O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC, 0644);
  termLine = name + " term\n";
  exitLine = name + " exit\n";
  lingers = mode.kind == "linger-term";
  lingerFor.tv_sec = static_cast<time_t>(mode.after.count() / 1000);
  lingerFor.tv_nsec = static_cast<long>(mode.after.count() % 1000) * 1'000'000L;
  struct sigaction action {};
  action.sa_handler = onTerm;
  sigemptyset(&action.sa_mask);
  ::sigaction(SIGTERM, &action, nullptr);
}

[[noreturn]] void forever() {
  for (;;)
    ::pause();
}

// polls fd until the mode's moment, then acts on it; returns when fd hangs up
void serve(int fd, Mode mode, const std::function<void()>& readable, const std::function<void()>& release) {
  for (;;) {
    int wait = -1;
    if (mode.at) {
      const auto left = std::chrono::duration_cast<std::chrono::milliseconds>(*mode.at - Clock::now()).count();
      wait = static_cast<int>(std::max<long long>(0, left));
    }
    pollfd ready{fd, POLLIN, 0};
    const int polled = ::poll(&ready, 1, wait);
    if (polled > 0)
      readable();
    if (mode.at && Clock::now() >= *mode.at) {
      mode.at.reset();
      if (mode.kind == "exit-after")
        std::exit(4);
      if (mode.kind == "release-after" && release)
        release();
    }
    if (polled > 0 && (ready.revents & (POLLHUP | POLLERR)) != 0)
      return;
  }
}

int ownName(const std::string& name, const Mode& mode) {
  const char* address = std::getenv("DBUS_SYSTEM_BUS_ADDRESS");
  if (address == nullptr)
    return 5;
  auto connected = cadence::dbus::Client::connect(address);
  if (!connected.ok())
    return 6;
  auto& client = connected.value();
  const auto reply = client.callAndWait(cadence::dbus::requestNameCall(name, cadence::dbus::kNameDoNotQueue));
  if (reply.status != cadence::dbus::CallStatus::Ok)
    return 7;
  serve(
      client.fd(), mode,
      [&] {
        do
          client.process();
        while (client.hasBuffered());
        // a daemon whose bus went lingers until it is killed, as a leak would
        if (!client.open())
          forever();
      },
      [&] {
        client.send(cadence::dbus::Message::methodCall(std::string(cadence::dbus::kBusName),
                                                       std::string(cadence::dbus::kBusPath),
                                                       std::string(cadence::dbus::kBusInterface), "ReleaseName",
                                                       {cadence::dbus::Value::string(name)}));
      });
  return 0;
}

int timing(const std::string& rig, const Mode& mode) {
  std::istringstream config(read(rig + "/timing"));
  int port = 0;
  std::string path;
  config >> port >> path;
  const int fd = ::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0);
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  address.sin_port = htons(static_cast<std::uint16_t>(port));
  if (::bind(fd, reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0)
    return 8;
  bool made = false;
  serve(
      fd, mode,
      [&] {
        char datagram[64]{};
        const auto size = ::recv(fd, datagram, sizeof datagram, 0);
        if (made || size != 9 || std::memcmp(datagram, "/nqptp T\0", 9) != 0)
          return;
        const std::uint16_t version = 11;
        std::ofstream out(path, std::ios::binary | std::ios::trunc);
        out.write(reinterpret_cast<const char*>(&version), sizeof version);
        out << std::string(30, '\0');
        made = true;
      },
      nullptr);
  return 0;
}

}  // namespace

int main(int argc, char** argv) {
  const std::vector<std::string> args(argv, argv + argc);
  const std::string self = args.front();
  const auto name = self.substr(self.rfind('/') + 1);
  const auto rig = dirOf(dirOf(dirOf(self))) + "/fake";
  const auto mode = modeOf(rig, name);
  std::istringstream timingConfig(read(rig + "/timing"));
  int port = 0;
  std::string timingPath;
  timingConfig >> port >> timingPath;
  struct stat info {};
  record(rig, name, args, !timingPath.empty() && ::lstat(timingPath.c_str(), &info) == 0);
  // a line on each of its streams, which audiod drains into its lines or leaves on /dev/null
  std::printf("%s: up\n", name.c_str());
  std::fflush(stdout);
  std::fprintf(stderr, "%s: complaint\n", name.c_str());
  if (mode.kind == "exit")
    return 3;
  if (mode.kind == "silent")
    forever();
  if (mode.kind == "linger-term" || mode.kind == "hold-term")
    trapTerm(rig, name, mode);
  if (name == "avahi-daemon")
    return ownName("org.freedesktop.Avahi", mode);
  if (name == "shairport-sync")
    return ownName("org.gnome.ShairportSync", mode);
  if (name == "nqptp")
    return timing(rig, mode);
  return 2;
}

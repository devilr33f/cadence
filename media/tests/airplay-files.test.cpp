#include "audiod/airplay-files.h"
#include "audiod/airplay-timing.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <chrono>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <string>

using namespace cadence::audiod;
using namespace cadence::audiod::airplay;
using namespace cadence::test_support;

namespace {

mode_t modeOf(const std::string& path) {
  struct stat info {};
  REQUIRE(::lstat(path.c_str(), &info) == 0);
  return info.st_mode;
}

std::string contents(const std::string& path) {
  std::ifstream in(path);
  std::stringstream text;
  text << in.rdbuf();
  return text.str();
}

// a udp port of 127.0.0.1 nothing holds, as far as the kernel says now
std::uint16_t freeUdpPort() {
  const UniqueFd fd(::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0));
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  socklen_t length = sizeof address;
  REQUIRE(::bind(fd.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
  REQUIRE(::getsockname(fd.get(), reinterpret_cast<sockaddr*>(&address), &length) == 0);
  return ntohs(address.sin_port);
}

UniqueFd boundUdp(std::uint16_t port, in_addr_t host) {
  UniqueFd fd(::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0));
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(host);
  address.sin_port = htons(port);
  REQUIRE(::bind(fd.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
  return fd;
}

void writeTiming(const std::string& path, std::uint16_t version) {
  std::ofstream out(path, std::ios::binary | std::ios::trunc);
  out.write(reinterpret_cast<const char*>(&version), sizeof version);
  out << std::string(30, '\0');
}

}  // namespace

TEST_CASE("a private dir is made 0700, kept when it is one, and refused as a symlink or a file") {
  const TempDir dir;
  const auto path = dir.file("a/b/scratch");
  REQUIRE_FALSE(privateDir(path));
  CHECK((modeOf(path) & 07777) == 0700);
  std::ofstream(path + "/kept") << "x";
  std::filesystem::permissions(path, std::filesystem::perms(0755));
  REQUIRE_FALSE(privateDir(path));
  CHECK((modeOf(path) & 07777) == 0700);
  CHECK(std::filesystem::exists(path + "/kept"));

  std::filesystem::create_directory_symlink(path, dir.file("link"));
  const auto link = privateDir(dir.file("link"));
  REQUIRE(link);
  CHECK(link->code == ELOOP);
  std::ofstream(dir.file("file")) << "x";
  const auto file = privateDir(dir.file("file"));
  REQUIRE(file);
  CHECK(file->code == ENOTDIR);
}

TEST_CASE("a fresh dir replaces whatever was there, never following a symlink in it") {
  const TempDir dir;
  const auto outside = dir.file("outside");
  std::filesystem::create_directories(outside);
  std::ofstream(outside + "/keep") << "x";
  const auto base = dir.file("avahi");
  std::filesystem::create_directories(base + "/etc/avahi");
  std::ofstream(base + "/etc/avahi/services") << "x";
  std::filesystem::create_directory_symlink(outside, base + "/link");
  REQUIRE_FALSE(freshDir(base));
  CHECK(std::filesystem::is_empty(base));
  CHECK((modeOf(base) & 07777) == 0700);
  CHECK(std::filesystem::exists(outside + "/keep"));

  // a symlink at the path itself goes, its target stays
  std::filesystem::remove(base);
  std::filesystem::create_directory_symlink(outside, base);
  REQUIRE_FALSE(freshDir(base));
  CHECK(S_ISDIR(modeOf(base)));
  CHECK(std::filesystem::exists(outside + "/keep"));
}

TEST_CASE("a private file is 0600 and never written through a symlink") {
  const TempDir dir;
  const auto path = dir.file("shairport.conf");
  std::ofstream(path) << "old and longer";
  std::filesystem::permissions(path, std::filesystem::perms(0644));
  REQUIRE_FALSE(writePrivate(path, "new"));
  CHECK(contents(path) == "new");
  CHECK((modeOf(path) & 07777) == 0600);

  std::ofstream(dir.file("target")) << "untouched";
  std::filesystem::create_symlink(dir.file("target"), dir.file("link"));
  CHECK(writePrivate(dir.file("link"), "evil"));
  CHECK(contents(dir.file("target")) == "untouched");
}

TEST_CASE("a stale bus socket goes, a symlink or a directory there is refused") {
  const TempDir dir;
  CHECK_FALSE(removeStale(dir.file("absent")));
  std::ofstream(dir.file("bus.sock")) << "";
  CHECK_FALSE(removeStale(dir.file("bus.sock")));
  CHECK_FALSE(std::filesystem::exists(dir.file("bus.sock")));
  std::filesystem::create_directory(dir.file("dir"));
  CHECK(removeStale(dir.file("dir")));
  std::filesystem::create_symlink(dir.file("dir"), dir.file("link"));
  CHECK(removeStale(dir.file("link")));
  CHECK(std::filesystem::exists(dir.file("dir")));
}

TEST_CASE("a fifo is made 0600, kept when it is one, and a regular file at its path refused") {
  const TempDir dir;
  const auto path = dir.file("audio.fifo");
  REQUIRE_FALSE(privateFifo(path));
  CHECK(S_ISFIFO(modeOf(path)));
  CHECK((modeOf(path) & 07777) == 0600);
  // shairport makes a missing one 0666: one already there keeps our mode, and ours is put back
  std::filesystem::permissions(path, std::filesystem::perms(0666));
  REQUIRE_FALSE(privateFifo(path));
  CHECK((modeOf(path) & 07777) == 0600);

  std::ofstream(dir.file("file")) << "audio would land here";
  const auto file = privateFifo(dir.file("file"));
  REQUIRE(file);
  CHECK(file->code == EEXIST);
  CHECK(contents(dir.file("file")) == "audio would land here");
  std::filesystem::create_symlink(path, dir.file("link"));
  const auto link = privateFifo(dir.file("link"));
  REQUIRE(link);
  CHECK(link->code == ELOOP);
}

TEST_CASE("a stale timing interface goes as a regular file only") {
  const TempDir dir;
  const auto path = dir.file("nqptp");
  CHECK_FALSE(removeStaleTiming(path));
  writeTiming(path, kTimingVersion);
  CHECK_FALSE(removeStaleTiming(path));
  CHECK_FALSE(std::filesystem::exists(path));

  std::filesystem::create_directory(dir.file("dir"));
  std::ofstream(dir.file("dir/inside")) << "x";
  std::filesystem::create_directory_symlink(dir.file("dir"), path);
  const auto link = removeStaleTiming(path);
  REQUIRE(link);
  CHECK(link->code == ELOOP);
  CHECK(std::filesystem::exists(dir.file("dir/inside")));
  std::filesystem::remove(path);
  std::filesystem::create_directory(path);
  CHECK(removeStaleTiming(path));
  CHECK(std::filesystem::is_directory(path));
}

TEST_CASE("the timing interface is fresh with its version and a stamp of its own") {
  const TempDir dir;
  const auto path = dir.file("nqptp");
  CHECK_FALSE(timingFresh(path, std::nullopt));
  writeTiming(path, kTimingVersion);
  const auto stamp = timingStamp(path);
  REQUIRE(stamp);
  CHECK(timingFresh(path, std::nullopt));
  CHECK_FALSE(timingFresh(path, stamp));
  std::filesystem::remove(path);
  writeTiming(path, kTimingVersion);
  // a kernel with a coarse file clock stamps two writes within a tick alike: this one is a second later, as a new
  // nqptp's would be
  std::filesystem::last_write_time(path, std::filesystem::last_write_time(path) + std::chrono::seconds(1));
  CHECK(timingFresh(path, stamp));
  writeTiming(path, kTimingVersion - 1);
  CHECK_FALSE(timingFresh(path, std::nullopt));
  // a symlink to a good interface is not one
  writeTiming(dir.file("real"), kTimingVersion);
  std::filesystem::remove(path);
  std::filesystem::create_symlink(dir.file("real"), path);
  CHECK_FALSE(timingFresh(path, std::nullopt));
  CHECK_FALSE(timingStamp(path));
}

TEST_CASE("nqptp's ports are held while free and refused while another holds one") {
  const TimingPorts ports{.event = freeUdpPort(), .general = freeUdpPort(), .control = freeUdpPort()};
  {
    auto held = PortHold::bind(ports);
    REQUIRE(held.ok());
    // held: nothing else takes the control port meanwhile
    const UniqueFd other(::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0));
    sockaddr_in address{};
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    address.sin_port = htons(ports.control);
    CHECK(::bind(other.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0);
  }
  for (const auto busy : {ports.event, ports.general}) {
    const auto holder = boundUdp(busy, INADDR_ANY);
    const auto refused = PortHold::bind(ports);
    REQUIRE_FALSE(refused.ok());
    CHECK(refused.error().code == EADDRINUSE);
  }
  const auto holder = boundUdp(ports.control, INADDR_LOOPBACK);
  const auto refused = PortHold::bind(ports);
  REQUIRE_FALSE(refused.ok());
  CHECK(refused.error().code == EADDRINUSE);
  // released with the hold: they are free again
  CHECK(PortHold::bind({.event = ports.event, .general = ports.general, .control = freeUdpPort()}).ok());
}

TEST_CASE("the nudge reaches the control port as nqptp's command") {
  const auto port = freeUdpPort();
  const auto listener = boundUdp(port, INADDR_LOOPBACK);
  const auto nudge = TimingNudge::open();
  REQUIRE(nudge);
  nudge->send(port);
  char received[32]{};
  const auto size = ::recv(listener.get(), received, sizeof received, 0);
  CHECK(std::string(received, static_cast<std::size_t>(std::max<ssize_t>(size, 0))) == std::string("/nqptp T\0", 9));
}

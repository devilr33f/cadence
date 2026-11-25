#include "audiod/airplay-config.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <filesystem>
#include <fstream>
#include <string>

using namespace cadence::audiod::airplay;
using namespace cadence::test_support;

TEST_CASE("a quoted value cannot leave its config string") {
  CHECK(quote("Kitchen") == "Kitchen");
  CHECK(quote("a\\b") == "a\\\\b");
  CHECK(quote("a\"b") == "a\\\"b");
  // shairport substitutes ${NAME} before libconfig parses; $${ is its literal spelling
  CHECK(quote("x${HOME}y") == "x$${HOME}y");
  CHECK(quote("$x{") == "$x{");
  CHECK(quote("Кухня") == "Кухня");
  CHECK_FALSE(quote("bad\nname"));
  CHECK_FALSE(quote(std::string("nul\0x", 5)));
  CHECK_FALSE(quote("del\x7f"));
  // a c1 control in utf-8
  CHECK_FALSE(quote("next\xc2\x85line"));
}

TEST_CASE("shairport's config pipes 48 kHz s16 stereo and its metadata into our fifos") {
  const auto config = shairportConfig("Kit\"chen ${X}", 0x0200000000ab, "/s/run/audio.fifo", "/s/run/metadata.fifo");
  REQUIRE(config);
  CHECK(*config == R"(general =
{
  name = "Kit\"chen $${X}";
  service_type = "airplay2";
  interpolation = "vernier";
  output_backend = "pipe";
  mdns_backend = "avahi";
  dbus_service_bus = "system";
  ignore_volume_control = "yes";
  airplay_device_id = 0x0200000000ABL;
  audio_backend_buffer_desired_length_in_seconds = 0.1;
};

pipe =
{
  name = "/s/run/audio.fifo";
  output_rate = 48000;
  output_format = "S16_LE";
  output_channels = 2;
};

metadata =
{
  enabled = "yes";
  include_cover_art = "no";
  cover_art_cache_directory = "";
  pipe_name = "/s/run/metadata.fifo";
};
)");
  CHECK_FALSE(shairportConfig("bad\tname", 1, "/a", "/b"));
  CHECK_FALSE(shairportConfig("ok", 1, "/a\n", "/b"));
}

TEST_CASE("the private bus listens on its socket alone, for audiod's uid alone") {
  CHECK(busConfig("/s/bus.sock", 0) ==
        "<!DOCTYPE busconfig PUBLIC \"-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN\" "
        "\"http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd\">\n"
        "<busconfig>\n"
        "  <type>system</type>\n"
        "  <listen>unix:path=/s/bus.sock</listen>\n"
        "  <auth>EXTERNAL</auth>\n"
        "  <policy context=\"default\">\n"
        "    <deny user=\"*\"/>\n"
        "    <allow user=\"0\"/>\n"
        "    <allow own=\"*\"/>\n"
        "    <allow send_destination=\"*\"/>\n"
        "    <allow receive_sender=\"*\"/>\n"
        "    <allow eavesdrop=\"true\"/>\n"
        "  </policy>\n"
        "</busconfig>\n");
  // the address escapes what d-bus must not see bare, and the xml what xml must not
  const auto odd = busConfig("/s/a&b c/bus.sock", 1000);
  CHECK(odd.find("<listen>unix:path=/s/a%26b%20c/bus.sock</listen>") != std::string::npos);
  CHECK(odd.find("<allow user=\"1000\"/>") != std::string::npos);
}

TEST_CASE("avahi publishes the receiver's addresses under its id and nothing else") {
  CHECK(avahiConfig(0x0200000000ab) ==
        "[server]\nhost-name=cadence-airplay-0200000000AB\nuse-ipv4=yes\nuse-ipv6=yes\nenable-dbus=yes\n"
        "disallow-other-stacks=no\nratelimit-interval-usec=1000000\nratelimit-burst=1000\n\n"
        "[wide-area]\nenable-wide-area=no\n\n"
        "[publish]\ndisable-publishing=no\npublish-addresses=yes\npublish-hinfo=no\npublish-workstation=no\n"
        "publish-domain=no\npublish-resolv-conf-dns-servers=no\n\n"
        "[reflector]\nenable-reflector=no\n");
}

TEST_CASE("addresses are six nonzero bytes, strictly") {
  CHECK(parseAddress("00:00:00:00:00:00") == std::nullopt);
  CHECK(parseAddress("01:02:03:04:05:06") == 0x010203040506);
  CHECK(parseAddress("0a:0B:03:04:05:06\n") == 0x0a0b03040506);
  CHECK(parseAddress("1:02:03:04:05:06") == std::nullopt);
  CHECK(parseAddress("01:02:03:04:05") == std::nullopt);
  CHECK(parseAddress("01:02:03:04:05:06:07") == std::nullopt);
  CHECK(parseAddress("gg:02:03:04:05:06") == std::nullopt);
  CHECK(parseAddress("01-02-03-04-05-06") == std::nullopt);
}

TEST_CASE("the receiver id skips loopback and zero addresses, in interface name order") {
  const TempDir root;
  const auto add = [&](const std::string& name, const std::string& flags, const std::string& address) {
    std::filesystem::create_directories(root.file(name));
    std::ofstream(root.file(name + "/flags")) << flags << '\n';
    std::ofstream(root.file(name + "/address")) << address << '\n';
  };
  add("wlan0", "0x1003", "06:00:00:00:00:01");
  add("lo", "0x9", "00:00:00:00:00:00");
  add("eth0", "0x1003", "02:00:00:00:00:01");
  add("aaaa0", "0x1003", "00:00:00:00:00:00");
  // a loopback with a real-looking address is still loopback
  add("abc0", "0x9", "0e:00:00:00:00:01");
  CHECK(receiverId(root.str()) == 0x020000000001);
  std::filesystem::remove_all(root.file("eth0"));
  CHECK(receiverId(root.str()) == 0x060000000001);
  std::filesystem::remove_all(root.file("wlan0"));
  CHECK(receiverId(root.str()) == std::nullopt);
  CHECK(receiverId(root.file("missing")) == std::nullopt);
}

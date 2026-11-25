#include "dbus/auth.h"

#include <catch2/catch_test_macros.hpp>

#include <string>

using namespace cadence::dbus;
using State = AuthClient::State;

namespace {

const std::string kOk = "OK 1234deadbeef1234deadbeef1234dead\r\n";

}  // namespace

TEST_CASE("the greeting is the nul byte and AUTH EXTERNAL with the uid in hex, as the specification's example") {
  CHECK(AuthClient(1000).greeting() == std::string("\0AUTH EXTERNAL 31303030\r\n", 25));
  CHECK(AuthClient(0).greeting() == std::string("\0AUTH EXTERNAL 30\r\n", 19));
}

TEST_CASE("OK is answered with BEGIN, never NEGOTIATE_UNIX_FD, also when it arrives byte by byte") {
  AuthClient whole(0);
  CHECK(whole.receive(kOk) == "BEGIN\r\n");
  CHECK(whole.state() == State::Authenticated);
  CHECK(whole.guid() == "1234deadbeef1234deadbeef1234dead");
  CHECK(whole.rest().empty());

  AuthClient split(0);
  std::string answer;
  for (const char byte : kOk)
    answer += split.receive(std::string(1, byte));
  CHECK(answer == "BEGIN\r\n");
  CHECK(split.state() == State::Authenticated);
}

TEST_CASE("bytes after the OK line are the message stream's") {
  AuthClient client(0);
  CHECK(client.receive(kOk + "l\x01") == "BEGIN\r\n");
  CHECK(client.rest() == "l\x01");
}

TEST_CASE("anything but OK to EXTERNAL fails, since there is no other mechanism to try") {
  for (const auto* reply : {"REJECTED EXTERNAL DBUS_COOKIE_SHA1\r\n", "REJECTED\r\n", "DATA 00\r\n", "ERROR\r\n",
                            "ERROR not today\r\n"}) {
    INFO(reply);
    AuthClient client(0);
    CHECK(client.receive(reply).empty());
    CHECK(client.state() == State::Failed);
  }
}

TEST_CASE("a command of a later specification is answered with ERROR, and the exchange goes on") {
  AuthClient client(0);
  CHECK(client.receive("EXTENSION_COM_EXAMPLE_HELLO there\r\n") == "ERROR\r\n");
  CHECK(client.state() == State::Waiting);
  CHECK(client.receive(kOk) == "BEGIN\r\n");
  CHECK(client.state() == State::Authenticated);
}

TEST_CASE("a broken server line fails the exchange") {
  for (const auto& reply :
       {std::string("OK\r\n"), std::string("OK xyz\r\n"), std::string("OK 12 34\r\n"), std::string("ok 1234\r\n"),
        std::string("OK 12\0" "34\r\n", 10), std::string("OK \xc3\xa9\r\n"), std::string("OK 1234\n"),
        std::string(AuthClient::kMaxLine + 1, 'A')}) {
    INFO(reply);
    AuthClient client(0);
    (void)client.receive(reply);
    CHECK(client.state() == State::Failed);
  }
}

TEST_CASE("nothing more is taken once the exchange ended") {
  AuthClient client(0);
  (void)client.receive("REJECTED\r\n");
  CHECK(client.receive(kOk).empty());
  CHECK(client.state() == State::Failed);
}

TEST_CASE("the socket path of a unix:path= address") {
  CHECK(unixSocketPath("unix:path=/tmp/dbus-test") == "/tmp/dbus-test");
  CHECK(unixSocketPath("unix:path=/tmp/cadence-airplay/bus.sock,guid=0123456789abcdef0123456789abcdef") ==
        "/tmp/cadence-airplay/bus.sock");
  CHECK(unixSocketPath("unix:guid=00,path=/run/a") == "/run/a");
  CHECK(unixSocketPath("unix:path=/tmp/with%20space%2c%3b") == "/tmp/with space,;");
  CHECK(unixSocketPath("tcp:host=127.0.0.1,port=4242;unix:path=/b") == "/b");
  CHECK(unixSocketPath("unix:path=/a;unix:path=/b") == "/a");
  for (const auto* refused : {"", "unix:", "unix:path=", "unix:abstract=/tmp/dbus-x", "tcp:host=127.0.0.1,port=1",
                              "unix:path=/a%2", "unix:path=/a%zz", "unix:path=/a b", "unix:path=/a%00b",
                              "path=/a", "unix:path"}) {
    INFO(refused);
    CHECK_FALSE(unixSocketPath(refused));
  }
}

TEST_CASE("the address of a socket path escapes what may not stand bare, and reads back as that path") {
  CHECK(unixAddress("/tmp/cadence-airplay/bus.sock") == "unix:path=/tmp/cadence-airplay/bus.sock");
  CHECK(unixAddress("/a b,c;d=e%f\\g") == "unix:path=/a%20b%2cc%3bd%3de%25f%5cg");
  for (const std::string path : {"/tmp/x.sock", "/a b,c;d=e%f\\g", "/\xc3\xa9t\xc3\xa9/*_-."}) {
    INFO(path);
    CHECK(unixSocketPath(unixAddress(path)) == path);
  }
}

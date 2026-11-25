#include "dbus/bus.h"
#include "dbus/client.h"
#include "support/dbus-daemon.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <poll.h>
#include <sys/eventfd.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#include <algorithm>
#include <chrono>
#include <optional>
#include <string>
#include <vector>

using namespace cadence::dbus;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

// empty when the build found no dbus-daemon
constexpr std::string_view kDaemon = CADENCE_DBUS_DAEMON;

#define REQUIRE_DAEMON()                                                                                 \
  do {                                                                                                   \
    if (kDaemon.empty())                                                                                 \
      SKIP("no dbus-daemon was found when this was configured (the builder's test stage installs one)"); \
  } while (false)

const std::string kService = "org.cadence.Test1";
const std::string kPath = "/org/cadence/Test1";
const std::string kInterface = "org.cadence.Test1";

Client connectedClient(const PrivateBus& bus, ConnectionOptions options = {}) {
  auto result = Client::connect(bus.address(), options);
  REQUIRE(result.ok());
  return std::move(result.value());
}

// processes the client until condition holds; false after limit
template <typename Condition>
bool pumpUntil(Client& client, Condition condition, std::chrono::milliseconds limit = 5s) {
  const auto deadline = Clock::now() + limit;
  while (!condition()) {
    if (Clock::now() > deadline)
      return false;
    pollfd ready{client.fd(), POLLIN, 0};
    ::poll(&ready, 1, 10);
    client.process();
  }
  return true;
}

Message call(const std::string& member, std::vector<Value> body = {}) {
  return Message::methodCall(kService, kPath, kInterface, member, std::move(body));
}

// the test service, run in a process of its own: Echo(s) -> s, Fail() -> an error, a read-write Volume (d) in
// [-144, 0] that emits PropertiesChanged when set, a read-only State (s). it writes a byte to readyFd once it owns its
// name and returns once stopFd turns readable
int serve(const std::string& address, int readyFd, int stopFd) {
  auto connected = Client::connect(address, {.stopFd = stopFd});
  if (!connected.ok())
    return 2;
  auto& client = connected.value();
  double volume = -20;
  Interface interface{.name = kInterface, .methods = {}, .properties = {}};
  interface.methods["Echo"] = [](const Message& message) {
    if (message.signature() != "s")
      return MethodResult{.values = {}, .errorName = "org.freedesktop.DBus.Error.InvalidArgs", .errorText = "s"};
    return MethodResult{.values = {message.body[0]}, .errorName = {}, .errorText = {}};
  };
  interface.methods["Fail"] = [](const Message&) {
    return MethodResult{.values = {}, .errorName = "org.cadence.Test1.Error.Refused", .errorText = "refused"};
  };
  interface.properties["Volume"] = Property{
      .get = [&volume] { return Value::float64(volume); },
      .set =
          [&volume, &client](const Value& value) {
            const auto level = value.asDouble();
            if (!level || *level < -144 || *level > 0)
              return false;
            volume = *level;
            client.emitPropertiesChanged(kPath, kInterface, {{"Volume", Value::float64(volume)}});
            return true;
          },
  };
  interface.properties["State"] = Property{.get = [] { return Value::string("Stopped"); }, .set = {}};
  client.exportInterface(kPath, std::move(interface));
  const auto owned = client.callAndWait(requestNameCall(kService, kNameDoNotQueue));
  if (owned.status != CallStatus::Ok || owned.body.size() != 1 || owned.body[0].asUint32() != kNamePrimaryOwner)
    return 3;
  const char ready = 1;
  if (::write(readyFd, &ready, 1) != 1)
    return 4;
  for (;;) {
    pollfd fds[] = {{client.fd(), POLLIN, 0}, {stopFd, POLLIN, 0}};
    ::poll(fds, 2, 100);
    if (fds[1].revents != 0)
      return 0;
    client.process();
    if (!client.open())
      return 5;
  }
}

// serve() in a forked process, there once it owns its name
class ServiceProcess {
 public:
  explicit ServiceProcess(const std::string& address) {
    int ready[2];
    int stop[2];
    REQUIRE(::pipe2(ready, O_CLOEXEC) == 0);
    REQUIRE(::pipe2(stop, O_CLOEXEC) == 0);
    pid_ = ::fork();
    REQUIRE(pid_ >= 0);
    if (pid_ == 0) {
      ::close(ready[0]);
      ::close(stop[1]);
      ::_exit(serve(address, ready[1], stop[0]));
    }
    ::close(ready[1]);
    ::close(stop[0]);
    stop_ = stop[1];
    pollfd readable{ready[0], POLLIN, 0};
    char byte = 0;
    const bool up = ::poll(&readable, 1, 5000) == 1 && ::read(ready[0], &byte, 1) == 1;
    ::close(ready[0]);
    REQUIRE(up);
  }
  ~ServiceProcess() {
    if (pid_ > 0) {
      ::kill(pid_, SIGKILL);
      ::waitpid(pid_, nullptr, 0);
    }
    if (stop_ >= 0)
      ::close(stop_);
  }
  ServiceProcess(const ServiceProcess&) = delete;
  ServiceProcess& operator=(const ServiceProcess&) = delete;

  // asks it to exit; its exit code
  int stop() {
    ::close(stop_);
    stop_ = -1;
    int status = 0;
    ::waitpid(pid_, &status, 0);
    pid_ = -1;
    return WIFEXITED(status) ? WEXITSTATUS(status) : -1;
  }

 private:
  pid_t pid_ = -1;
  int stop_ = -1;
};

std::vector<std::string> strings(const Value& array) {
  std::vector<std::string> out;
  for (const auto& item : array.items())
    if (const auto* text = item.asString())
      out.push_back(*text);
  return out;
}

bool contains(const std::vector<std::string>& names, const std::string& name) {
  return std::find(names.begin(), names.end(), name) != names.end();
}

Message volumeChanged(double level) {
  return Message::signal(kPath, std::string(kPropertiesInterface), "PropertiesChanged",
                         {Value::string(kInterface), Value::propertyMap({{"Volume", Value::float64(level)}}),
                          Value::array("s", {})});
}

}  // namespace

TEST_CASE("PropertiesChanged counts only from the expected unique name about the expected path") {
  auto signal = volumeChanged(-10);
  signal.sender = ":1.7";
  const auto genuine = parsePropertiesChangedFrom(signal, ":1.7", kPath);
  REQUIRE(genuine);
  CHECK(genuine->sender == ":1.7");
  CHECK(genuine->path == kPath);
  CHECK(genuine->interface == kInterface);
  CHECK(genuine->changed == std::vector<std::pair<std::string, Value>>{{"Volume", Value::float64(-10)}});
  CHECK(parsePropertiesChanged(signal)->sender == ":1.7");

  CHECK_FALSE(parsePropertiesChangedFrom(signal, ":1.8", kPath));
  CHECK_FALSE(parsePropertiesChangedFrom(signal, ":1.7", "/org/cadence/Other1"));
  // no message carries a well-known name as its sender: expecting one accepts nothing
  auto wellKnown = signal;
  wellKnown.sender = kService;
  CHECK_FALSE(parsePropertiesChangedFrom(wellKnown, kService, kPath));
  auto anonymous = signal;
  anonymous.sender.clear();
  CHECK_FALSE(parsePropertiesChangedFrom(anonymous, "", kPath));
}

TEST_CASE("NameOwnerChanged counts only from the bus itself") {
  auto signal = Message::signal(std::string(kBusPath), std::string(kBusInterface), "NameOwnerChanged",
                                {Value::string(kService), Value::string(":1.7"), Value::string("")});
  signal.sender = std::string(kBusName);
  const auto change = parseNameOwnerChanged(signal);
  REQUIRE(change);
  CHECK(change->name == kService);
  CHECK(change->oldOwner == ":1.7");
  CHECK(change->newOwner.empty());

  auto lookAlike = signal;
  lookAlike.sender = ":1.8";
  CHECK_FALSE(parseNameOwnerChanged(lookAlike));
  auto unsent = signal;
  unsent.sender.clear();
  CHECK_FALSE(parseNameOwnerChanged(unsent));
}

TEST_CASE("match rules take only valid names and paths, so no quote gets in") {
  CHECK(propertiesChangedRule(kService, kPath) ==
        "type='signal',sender='org.cadence.Test1',interface='org.freedesktop.DBus.Properties',"
        "member='PropertiesChanged',path='/org/cadence/Test1'");
  CHECK(propertiesChangedRule(":1.7", kPath).has_value());
  CHECK(nameOwnerChangedRule(kService) ==
        "type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged',"
        "arg0='org.cadence.Test1'");
  for (const auto* sender : {"org.a'B", "org.a.B',path='/x", "org.a.B\\", "nodots", "", ":"}) {
    INFO(sender);
    CHECK_FALSE(propertiesChangedRule(sender, kPath));
    CHECK_FALSE(nameOwnerChangedRule(sender));
  }
  for (const auto* path : {"/a'", "/a',sender='x", "relative", "/a/", ""}) {
    INFO(path);
    CHECK_FALSE(propertiesChangedRule(kService, path));
  }
}

TEST_CASE("a real bus: EXTERNAL, Hello and a unique name, then ListNames") {
  REQUIRE_DAEMON();
  PrivateBus bus{std::string(kDaemon)};
  auto client = connectedClient(bus);
  CHECK(client.uniqueName().starts_with(":"));
  CHECK(validBusName(client.uniqueName()));
  const auto names = client.callAndWait(listNamesCall());
  REQUIRE(names.status == CallStatus::Ok);
  REQUIRE(names.body.size() == 1);
  CHECK(contains(strings(names.body[0]), "org.freedesktop.DBus"));
  CHECK(contains(strings(names.body[0]), client.uniqueName()));
}

TEST_CASE("a service process and a client exchange method calls, replies and errors") {
  REQUIRE_DAEMON();
  PrivateBus bus{std::string(kDaemon)};
  ServiceProcess service(bus.address());
  auto client = connectedClient(bus);

  const auto echo = client.callAndWait(call("Echo", {Value::string("hello")}));
  CHECK(echo.status == CallStatus::Ok);
  CHECK(echo.body == std::vector<Value>{Value::string("hello")});

  const auto fail = client.callAndWait(call("Fail"));
  CHECK(fail.status == CallStatus::Error);
  CHECK(fail.errorName == "org.cadence.Test1.Error.Refused");
  CHECK(fail.body == std::vector<Value>{Value::string("refused")});

  CHECK(client.callAndWait(call("Nope")).errorName == "org.freedesktop.DBus.Error.UnknownMethod");
  CHECK(client.callAndWait(call("Echo", {Value::uint32(1)})).errorName == "org.freedesktop.DBus.Error.InvalidArgs");
  CHECK(client.callAndWait(Message::methodCall(kService, "/elsewhere", kInterface, "Echo"))
            .errorName == "org.freedesktop.DBus.Error.UnknownObject");
  CHECK(client.callAndWait(Message::methodCall(kService, kPath, "org.cadence.Other1", "Echo"))
            .errorName == "org.freedesktop.DBus.Error.UnknownInterface");
  CHECK(client.callAndWait(Message::methodCall(kService, "/anywhere", std::string(kPeerInterface), "Ping")).status ==
        CallStatus::Ok);
  // without an interface the member is looked up in the object's interfaces
  CHECK(client.callAndWait(Message::methodCall(kService, kPath, "", "Echo", {Value::string("bare")})).body ==
        std::vector<Value>{Value::string("bare")});

  // two calls in flight, each reply to its own handler by its serial
  std::vector<std::string> replies;
  for (const auto* text : {"first", "second"}) {
    CHECK(client.call(call("Echo", {Value::string(text)}), [&replies](Reply reply) {
      replies.push_back(reply.status == CallStatus::Ok ? *reply.body.at(0).asString() : "failed");
    }) != 0);
  }
  REQUIRE(pumpUntil(client, [&] { return replies.size() == 2; }));
  CHECK(replies == std::vector<std::string>{"first", "second"});
  CHECK_FALSE(client.nextDeadline());

  CHECK(service.stop() == 0);
}

TEST_CASE("properties: Get, Set and GetAll, and PropertiesChanged to a match on the sender and path") {
  REQUIRE_DAEMON();
  PrivateBus bus{std::string(kDaemon)};
  ServiceProcess service(bus.address());
  auto client = connectedClient(bus);
  std::vector<PropertiesChanged> changes;
  client.setSignalHandler([&changes](const Message& signal) {
    if (auto change = parsePropertiesChanged(signal))
      changes.push_back(std::move(*change));
  });
  const auto rule = propertiesChangedRule(kService, kPath);
  REQUIRE(rule);
  REQUIRE(client.callAndWait(addMatchCall(*rule)).status == CallStatus::Ok);

  const auto volume = client.callAndWait(propertyGetCall(kService, kPath, kInterface, "Volume"));
  REQUIRE(volume.status == CallStatus::Ok);
  CHECK(volume.body == std::vector<Value>{Value::variant(Value::float64(-20))});

  const auto all = client.callAndWait(propertyGetAllCall(kService, kPath, kInterface));
  REQUIRE(all.status == CallStatus::Ok);
  REQUIRE(all.body.size() == 1);
  const auto entries = parsePropertyMap(all.body[0]);
  REQUIRE(entries);
  CHECK(*entries == std::vector<std::pair<std::string, Value>>{{"State", Value::string("Stopped")},
                                                               {"Volume", Value::float64(-20)}});
  const auto other = client.callAndWait(propertyGetAllCall(kService, kPath, "org.cadence.Other1"));
  CHECK(other.body == std::vector<Value>{Value::propertyMap({})});

  CHECK(client.callAndWait(propertySetCall(kService, kPath, kInterface, "Volume", Value::float64(-15.5))).status ==
        CallStatus::Ok);
  REQUIRE(pumpUntil(client, [&] { return !changes.empty(); }));
  REQUIRE(changes.size() == 1);
  CHECK(changes[0].interface == kInterface);
  CHECK(changes[0].changed == std::vector<std::pair<std::string, Value>>{{"Volume", Value::float64(-15.5)}});
  CHECK(changes[0].invalidated.empty());
  CHECK(client.callAndWait(propertyGetCall(kService, kPath, kInterface, "Volume")).body ==
        std::vector<Value>{Value::variant(Value::float64(-15.5))});

  CHECK(client.callAndWait(propertySetCall(kService, kPath, kInterface, "State", Value::string("Playing")))
            .errorName == "org.freedesktop.DBus.Error.PropertyReadOnly");
  CHECK(client.callAndWait(propertySetCall(kService, kPath, kInterface, "Volume", Value::float64(3)))
            .errorName == "org.freedesktop.DBus.Error.InvalidArgs");
  CHECK(client.callAndWait(propertyGetCall(kService, kPath, kInterface, "Nope")).errorName ==
        "org.freedesktop.DBus.Error.UnknownProperty");
  CHECK(changes.size() == 1);
}

TEST_CASE("a PropertiesChanged another peer sends to our own name passes the match rules, not the sender check") {
  REQUIRE_DAEMON();
  PrivateBus bus{std::string(kDaemon)};
  ServiceProcess service(bus.address());
  auto client = connectedClient(bus);
  auto forger = connectedClient(bus);
  const auto owner = client.callAndWait(getNameOwnerCall(kService));
  REQUIRE(owner.status == CallStatus::Ok);
  const auto unique = *owner.body.at(0).asString();
  std::vector<PropertiesChanged> seen;
  std::vector<PropertiesChanged> accepted;
  client.setSignalHandler([&](const Message& signal) {
    if (auto any = parsePropertiesChanged(signal))
      seen.push_back(std::move(*any));
    if (auto from = parsePropertiesChangedFrom(signal, unique, kPath))
      accepted.push_back(std::move(*from));
  });
  const auto rule = propertiesChangedRule(kService, kPath);
  REQUIRE(rule);
  REQUIRE(client.callAndWait(addMatchCall(*rule)).status == CallStatus::Ok);

  // unicast: the bus delivers it whatever our match rules say
  auto forged = volumeChanged(0);
  forged.destination = client.uniqueName();
  REQUIRE(forger.send(forged));
  REQUIRE(pumpUntil(client, [&] { return seen.size() == 1; }));
  CHECK(seen[0].sender == forger.uniqueName());
  CHECK(seen[0].path == kPath);
  CHECK(accepted.empty());

  REQUIRE(client.callAndWait(propertySetCall(kService, kPath, kInterface, "Volume", Value::float64(-15.5))).status ==
          CallStatus::Ok);
  REQUIRE(pumpUntil(client, [&] { return accepted.size() == 1; }));
  CHECK(accepted[0].sender == unique);
  CHECK(accepted[0].changed == std::vector<std::pair<std::string, Value>>{{"Volume", Value::float64(-15.5)}});
  CHECK(seen.size() == 2);
}

TEST_CASE("NameOwnerChanged when the service exits, NameHasOwner and GetNameOwner following it") {
  REQUIRE_DAEMON();
  PrivateBus bus{std::string(kDaemon)};
  ServiceProcess service(bus.address());
  auto client = connectedClient(bus);
  std::vector<NameOwnerChanged> changes;
  client.setSignalHandler([&changes](const Message& signal) {
    if (auto change = parseNameOwnerChanged(signal))
      changes.push_back(std::move(*change));
  });
  const auto rule = nameOwnerChangedRule(kService);
  REQUIRE(rule);
  REQUIRE(client.callAndWait(addMatchCall(*rule)).status == CallStatus::Ok);

  CHECK(client.callAndWait(nameHasOwnerCall(kService)).body == std::vector<Value>{Value::boolean(true)});
  const auto owner = client.callAndWait(getNameOwnerCall(kService));
  REQUIRE(owner.status == CallStatus::Ok);
  REQUIRE(owner.body.size() == 1);
  const auto unique = *owner.body[0].asString();
  CHECK(unique.starts_with(":"));
  CHECK(unique != client.uniqueName());

  CHECK(service.stop() == 0);
  REQUIRE(pumpUntil(client, [&] { return !changes.empty(); }));
  CHECK(changes[0].name == kService);
  CHECK(changes[0].oldOwner == unique);
  CHECK(changes[0].newOwner.empty());
  CHECK(client.callAndWait(nameHasOwnerCall(kService)).body == std::vector<Value>{Value::boolean(false)});
  CHECK(client.callAndWait(getNameOwnerCall(kService)).errorName == "org.freedesktop.DBus.Error.NameHasNoOwner");
}

TEST_CASE("a call nobody answers times out; the default timeout is 2 s") {
  REQUIRE_DAEMON();
  CHECK(kDefaultCallTimeout == 2000ms);
  PrivateBus bus{std::string(kDaemon)};
  // owns a name and never reads again
  auto silent = connectedClient(bus);
  REQUIRE(silent.callAndWait(requestNameCall("org.cadence.Silent1", 0)).status == CallStatus::Ok);
  auto client = connectedClient(bus);
  const auto hang = Message::methodCall("org.cadence.Silent1", "/a", "org.cadence.Silent1", "Hang");

  const auto start = Clock::now();
  const auto reply = client.callAndWait(hang, 200ms);
  const auto elapsed = Clock::now() - start;
  CHECK(reply.status == CallStatus::Timeout);
  CHECK(elapsed >= 200ms);
  CHECK(elapsed < 2s);

  std::optional<Reply> late;
  REQUIRE(client.call(hang, [&late](Reply answer) { late = std::move(answer); }, 100ms) != 0);
  REQUIRE(client.nextDeadline());
  CHECK(*client.nextDeadline() <= Clock::now() + 100ms);
  REQUIRE(pumpUntil(client, [&] { return late.has_value(); }));
  CHECK(late->status == CallStatus::Timeout);
  CHECK_FALSE(client.nextDeadline());
  CHECK(client.open());
}

TEST_CASE("the bus going away: pending calls get Disconnected and the close code says hangup") {
  REQUIRE_DAEMON();
  PrivateBus bus{std::string(kDaemon)};
  auto silent = connectedClient(bus);
  REQUIRE(silent.callAndWait(requestNameCall("org.cadence.Silent1", 0)).status == CallStatus::Ok);
  auto client = connectedClient(bus);
  std::optional<Reply> reply;
  REQUIRE(client.call(Message::methodCall("org.cadence.Silent1", "/a", "org.cadence.Silent1", "Hang"),
                      [&reply](Reply answer) { reply = std::move(answer); }, 10s) != 0);
  bus.kill();
  REQUIRE(pumpUntil(client, [&] { return reply.has_value(); }));
  CHECK(reply->status == CallStatus::Disconnected);
  CHECK_FALSE(client.open());
  CHECK(client.closeCode() == CloseCode::Hangup);
  CHECK(client.callAndWait(listNamesCall()).status == CallStatus::Disconnected);
}

TEST_CASE("callAndWait gives up once the stop fd turns readable") {
  REQUIRE_DAEMON();
  PrivateBus bus{std::string(kDaemon)};
  auto silent = connectedClient(bus);
  REQUIRE(silent.callAndWait(requestNameCall("org.cadence.Silent1", 0)).status == CallStatus::Ok);
  const int stop = ::eventfd(0, EFD_CLOEXEC);
  REQUIRE(stop >= 0);
  auto client = connectedClient(bus, {.connectTimeout = 2s, .sendTimeout = 2s, .stopFd = stop});
  const std::uint64_t one = 1;
  REQUIRE(::write(stop, &one, sizeof one) == sizeof one);
  const auto start = Clock::now();
  const auto reply =
      client.callAndWait(Message::methodCall("org.cadence.Silent1", "/a", "org.cadence.Silent1", "Hang"), 5s);
  CHECK(reply.status == CallStatus::Stopped);
  CHECK(Clock::now() - start < 1s);
  CHECK_FALSE(client.nextDeadline());
  ::close(stop);
}

#include "dbus/client.h"

#include "dbus/bus.h"

#include <poll.h>

#include <cerrno>

namespace cadence::dbus {

namespace {

constexpr std::string_view kUnknownObject = "org.freedesktop.DBus.Error.UnknownObject";
constexpr std::string_view kUnknownInterface = "org.freedesktop.DBus.Error.UnknownInterface";
constexpr std::string_view kUnknownMethod = "org.freedesktop.DBus.Error.UnknownMethod";
constexpr std::string_view kUnknownProperty = "org.freedesktop.DBus.Error.UnknownProperty";
constexpr std::string_view kPropertyReadOnly = "org.freedesktop.DBus.Error.PropertyReadOnly";
constexpr std::string_view kInvalidArgs = "org.freedesktop.DBus.Error.InvalidArgs";

Reply status(CallStatus status) {
  Reply reply;
  reply.status = status;
  return reply;
}

MethodResult refusal(std::string_view name, std::string_view text) {
  return MethodResult{.values = {}, .errorName = std::string(name), .errorText = std::string(text)};
}

// the property of an interface named interfaceName (any, when empty)
const Property* findProperty(const std::vector<Interface>& interfaces, std::string_view interfaceName,
                             std::string_view name) {
  for (const auto& interface : interfaces) {
    if (!interfaceName.empty() && interface.name != interfaceName)
      continue;
    const auto found = interface.properties.find(name);
    if (found != interface.properties.end())
      return &found->second;
  }
  return nullptr;
}

}  // namespace

Result<Client, ConnectError> Client::connect(std::string_view address, ConnectionOptions options) {
  const auto deadline = Clock::now() + options.connectTimeout;
  auto opened = Connection::connect(address, options);
  if (!opened.ok())
    return opened.error();
  Client client(std::move(opened.value()));
  const auto serial = client.connection_.send(helloCall());
  if (serial == 0)
    return ConnectError::Hello;
  for (;;) {
    // nothing comes before Hello's answer but what the bus sends anyone; it is dropped
    while (auto message = client.connection_.next()) {
      if (message->replySerial != serial ||
          (message->type != MessageType::MethodReturn && message->type != MessageType::Error))
        continue;
      if (message->type != MessageType::MethodReturn || message->body.size() != 1 ||
          message->body[0].asString() == nullptr || !message->body[0].asString()->starts_with(':') ||
          !validBusName(*message->body[0].asString()))
        return ConnectError::Hello;
      client.uniqueName_ = *message->body[0].asString();
      return client;
    }
    if (!client.connection_.open())
      return ConnectError::Hello;
    if (const auto wait = waitFor(client.fd(), POLLIN, options.stopFd, deadline); wait != Wait::Ready)
      return wait == Wait::Timeout ? ConnectError::Timeout
                                   : (wait == Wait::Stopped ? ConnectError::Stopped : ConnectError::Connect);
  }
}

std::uint32_t Client::call(Message message, ReplyHandler handler, std::chrono::milliseconds timeout) {
  if (message.type != MessageType::MethodCall)
    return 0;
  message.flags = static_cast<std::uint8_t>(message.flags & ~kFlagNoReplyExpected);
  const auto serial = connection_.send(std::move(message));
  if (serial == 0)
    return 0;
  pending_.insert_or_assign(serial, Pending{std::move(handler), Clock::now() + timeout});
  return serial;
}

Reply Client::callAndWait(Message message, std::chrono::milliseconds timeout) {
  std::optional<Reply> result;
  const auto serial = call(std::move(message), [&result](Reply reply) { result = std::move(reply); }, timeout);
  if (serial == 0)
    return status(open() ? CallStatus::Invalid : CallStatus::Disconnected);
  const int stopFd = connection_.options().stopFd;
  while (!result) {
    const auto deadline = nextDeadline();
    // poll skips a closed connection's -1, and process() then fails the call; what is already read is not waited for
    pollfd fds[] = {{fd(), POLLIN, 0}, {stopFd, POLLIN, 0}};
    const int wait = hasBuffered() ? 0 : pollTimeout(deadline ? &*deadline : nullptr);
    if (::poll(fds, 2, wait) < 0 && errno != EINTR) {
      pending_.erase(serial);
      return status(CallStatus::Disconnected);
    }
    if (fds[1].revents != 0) {
      pending_.erase(serial);
      return status(CallStatus::Stopped);
    }
    process();
  }
  return std::move(*result);
}

bool Client::send(Message message) {
  if (message.type == MessageType::MethodCall)
    message.flags = static_cast<std::uint8_t>(message.flags | kFlagNoReplyExpected);
  return connection_.send(std::move(message)) != 0;
}

void Client::exportInterface(std::string path, Interface interface) {
  auto& interfaces = objects_[std::move(path)];
  for (auto& existing : interfaces) {
    if (existing.name == interface.name) {
      existing = std::move(interface);
      return;
    }
  }
  interfaces.push_back(std::move(interface));
}

bool Client::emitPropertiesChanged(std::string path, std::string interface,
                                   std::vector<std::pair<std::string, Value>> changed,
                                   std::vector<std::string> invalidated) {
  std::vector<Value> names;
  names.reserve(invalidated.size());
  for (auto& name : invalidated)
    names.push_back(Value::string(std::move(name)));
  return send(Message::signal(std::move(path), std::string(kPropertiesInterface), "PropertiesChanged",
                              {Value::string(std::move(interface)), Value::propertyMap(std::move(changed)),
                               Value::array("s", std::move(names))}));
}

void Client::process() {
  while (auto message = connection_.next())
    dispatch(std::move(*message));
  if (!connection_.open())
    failPending();
  expire(Clock::now());
}

std::optional<Clock::time_point> Client::nextDeadline() const {
  std::optional<Clock::time_point> earliest;
  for (const auto& [serial, pending] : pending_)
    if (!earliest || pending.deadline < *earliest)
      earliest = pending.deadline;
  return earliest;
}

void Client::close() { connection_.close(); }

void Client::dispatch(Message message) {
  switch (message.type) {
    case MessageType::MethodReturn:
    case MessageType::Error: {
      const auto found = pending_.find(*message.replySerial);
      if (found == pending_.end())
        return;
      // out of the map first: the handler may call again
      auto handler = std::move(found->second.handler);
      pending_.erase(found);
      Reply reply;
      reply.status = message.type == MessageType::Error ? CallStatus::Error : CallStatus::Ok;
      reply.errorName = std::move(message.errorName);
      reply.body = std::move(message.body);
      if (handler)
        handler(std::move(reply));
      return;
    }
    case MessageType::Signal:
      if (signalHandler_) {
        // a copy: the handler may replace itself
        const auto handler = signalHandler_;
        handler(message);
      }
      return;
    case MessageType::MethodCall:
      answer(message);
      return;
  }
  // a type of a later specification
}

void Client::answer(const Message& call) {
  MethodResult result;
  if (call.interface == kPeerInterface && call.member == "Ping") {
    // answered on any path, as the specification asks
  } else if (const auto object = objects_.find(call.path); object == objects_.end()) {
    result = refusal(kUnknownObject, "no such object");
  } else if (call.interface == kPropertiesInterface) {
    result = properties(call, object->second);
  } else {
    result = refusal(call.interface.empty() ? kUnknownMethod : kUnknownInterface, "no such interface");
    for (const auto& interface : object->second) {
      if (!call.interface.empty() && interface.name != call.interface)
        continue;
      const auto method = interface.methods.find(call.member);
      if (method != interface.methods.end()) {
        // a copy: the handler may export, replacing what it runs from
        const auto handler = method->second;
        result = handler ? handler(call) : refusal(kUnknownMethod, "no such method");
        break;
      }
      if (!call.interface.empty()) {
        result = refusal(kUnknownMethod, "no such method");
        break;
      }
    }
  }
  if ((call.flags & kFlagNoReplyExpected) != 0)
    return;
  if (!result.errorName.empty())
    send(Message::error(call, std::move(result.errorName), std::move(result.errorText)));
  else
    send(Message::methodReturn(call, std::move(result.values)));
}

MethodResult Client::properties(const Message& call, const std::vector<Interface>& interfaces) {
  const auto signature = call.signature();
  if (call.member == "Get" && signature == "ss") {
    const auto* property = findProperty(interfaces, *call.body[0].asString(), *call.body[1].asString());
    if (property == nullptr || !property->get)
      return refusal(kUnknownProperty, "no such property");
    const auto get = property->get;
    return MethodResult{.values = {Value::variant(get())}, .errorName = {}, .errorText = {}};
  }
  if (call.member == "GetAll" && signature == "s") {
    std::vector<std::pair<std::string, std::function<Value()>>> getters;
    for (const auto& interface : interfaces)
      if (interface.name == *call.body[0].asString())
        for (const auto& [name, property] : interface.properties)
          if (property.get)
            getters.emplace_back(name, property.get);
    std::vector<std::pair<std::string, Value>> entries;
    for (auto& [name, get] : getters)
      entries.emplace_back(std::move(name), get());
    return MethodResult{.values = {Value::propertyMap(std::move(entries))}, .errorName = {}, .errorText = {}};
  }
  if (call.member == "Set" && signature == "ssv") {
    const auto* property = findProperty(interfaces, *call.body[0].asString(), *call.body[1].asString());
    if (property == nullptr)
      return refusal(kUnknownProperty, "no such property");
    if (!property->set)
      return refusal(kPropertyReadOnly, "read-only property");
    const auto set = property->set;
    if (!set(*call.body[2].inner()))
      return refusal(kInvalidArgs, "value refused");
    return {};
  }
  if (call.member == "Get" || call.member == "GetAll" || call.member == "Set")
    return refusal(kInvalidArgs, "wrong arguments");
  return refusal(kUnknownMethod, "no such method");
}

void Client::expire(Clock::time_point now) {
  std::vector<std::uint32_t> due;
  for (const auto& [serial, pending] : pending_)
    if (pending.deadline <= now)
      due.push_back(serial);
  for (const auto serial : due) {
    const auto found = pending_.find(serial);
    if (found == pending_.end())
      continue;
    auto handler = std::move(found->second.handler);
    pending_.erase(found);
    if (handler)
      handler(status(CallStatus::Timeout));
  }
}

void Client::failPending() {
  auto pending = std::exchange(pending_, {});
  for (auto& [serial, call] : pending)
    if (call.handler)
      call.handler(status(CallStatus::Disconnected));
}

}  // namespace cadence::dbus

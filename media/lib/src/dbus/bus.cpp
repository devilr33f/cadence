#include "dbus/bus.h"

namespace cadence::dbus {

namespace {

Message busCall(std::string member, std::vector<Value> body = {}) {
  return Message::methodCall(std::string(kBusName), std::string(kBusPath), std::string(kBusInterface),
                             std::move(member), std::move(body));
}

Message propertiesCall(std::string destination, std::string path, std::string member, std::vector<Value> body) {
  return Message::methodCall(std::move(destination), std::move(path), std::string(kPropertiesInterface),
                             std::move(member), std::move(body));
}

}  // namespace

Message helloCall() { return busCall("Hello"); }

Message listNamesCall() { return busCall("ListNames"); }

Message requestNameCall(std::string name, std::uint32_t flags) {
  return busCall("RequestName", {Value::string(std::move(name)), Value::uint32(flags)});
}

Message nameHasOwnerCall(std::string name) { return busCall("NameHasOwner", {Value::string(std::move(name))}); }

Message getNameOwnerCall(std::string name) { return busCall("GetNameOwner", {Value::string(std::move(name))}); }

Message addMatchCall(std::string rule) { return busCall("AddMatch", {Value::string(std::move(rule))}); }

Message propertyGetCall(std::string destination, std::string path, std::string interface, std::string name) {
  return propertiesCall(std::move(destination), std::move(path), "Get",
                        {Value::string(std::move(interface)), Value::string(std::move(name))});
}

Message propertyGetAllCall(std::string destination, std::string path, std::string interface) {
  return propertiesCall(std::move(destination), std::move(path), "GetAll", {Value::string(std::move(interface))});
}

Message propertySetCall(std::string destination, std::string path, std::string interface, std::string name,
                        Value value) {
  return propertiesCall(std::move(destination), std::move(path), "Set",
                        {Value::string(std::move(interface)), Value::string(std::move(name)),
                         Value::variant(std::move(value))});
}

std::optional<std::string> propertiesChangedRule(std::string_view sender, std::string_view path) {
  if (!validBusName(sender) || !validObjectPath(path))
    return std::nullopt;
  std::string rule = "type='signal',sender='";
  rule += sender;
  rule += "',interface='";
  rule += kPropertiesInterface;
  rule += "',member='PropertiesChanged',path='";
  rule += path;
  rule += "'";
  return rule;
}

std::optional<std::string> nameOwnerChangedRule(std::string_view name) {
  if (!validBusName(name))
    return std::nullopt;
  std::string rule = "type='signal',sender='";
  rule += kBusName;
  rule += "',interface='";
  rule += kBusInterface;
  rule += "',member='NameOwnerChanged',arg0='";
  rule += name;
  rule += "'";
  return rule;
}

std::optional<PropertiesChanged> parsePropertiesChanged(const Message& signal) {
  if (signal.type != MessageType::Signal || signal.interface != kPropertiesInterface ||
      signal.member != "PropertiesChanged" || signal.signature() != "sa{sv}as")
    return std::nullopt;
  auto changed = parsePropertyMap(signal.body[1]);
  if (!changed)
    return std::nullopt;
  PropertiesChanged out;
  out.sender = signal.sender;
  out.path = signal.path;
  out.interface = *signal.body[0].asString();
  out.changed = std::move(*changed);
  for (const auto& name : signal.body[2].items())
    out.invalidated.push_back(*name.asString());
  return out;
}

std::optional<PropertiesChanged> parsePropertiesChangedFrom(const Message& signal, std::string_view sender,
                                                            std::string_view path) {
  if (!sender.starts_with(':') || !validBusName(sender) || signal.sender != sender || signal.path != path)
    return std::nullopt;
  return parsePropertiesChanged(signal);
}

std::optional<NameOwnerChanged> parseNameOwnerChanged(const Message& signal) {
  // only the bus can send as org.freedesktop.DBus; a peer's look-alike is not it
  if (signal.type != MessageType::Signal || signal.sender != kBusName || signal.interface != kBusInterface ||
      signal.member != "NameOwnerChanged" || signal.signature() != "sss")
    return std::nullopt;
  return NameOwnerChanged{
      .name = *signal.body[0].asString(),
      .oldOwner = *signal.body[1].asString(),
      .newOwner = *signal.body[2].asString(),
  };
}

std::optional<std::vector<std::pair<std::string, Value>>> parsePropertyMap(const Value& value) {
  if (value.type() != "a{sv}")
    return std::nullopt;
  std::vector<std::pair<std::string, Value>> entries;
  entries.reserve(value.items().size());
  for (const auto& entry : value.items()) {
    if (entry.items().size() != 2 || entry.items()[0].asString() == nullptr || entry.items()[1].inner() == nullptr)
      return std::nullopt;
    entries.emplace_back(*entry.items()[0].asString(), *entry.items()[1].inner());
  }
  return entries;
}

}  // namespace cadence::dbus

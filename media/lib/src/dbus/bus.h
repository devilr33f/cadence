#pragma once

#include "dbus/marshal.h"
#include "dbus/message.h"

#include <cstdint>
#include <optional>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

// the message bus's own interface and org.freedesktop.DBus.Properties, as the specification defines them
namespace cadence::dbus {

inline constexpr std::string_view kBusName = "org.freedesktop.DBus";
inline constexpr std::string_view kBusPath = "/org/freedesktop/DBus";
inline constexpr std::string_view kBusInterface = "org.freedesktop.DBus";
inline constexpr std::string_view kPropertiesInterface = "org.freedesktop.DBus.Properties";
inline constexpr std::string_view kPeerInterface = "org.freedesktop.DBus.Peer";

// RequestName's flags and answers
inline constexpr std::uint32_t kNameAllowReplacement = 0x1;
inline constexpr std::uint32_t kNameReplaceExisting = 0x2;
inline constexpr std::uint32_t kNameDoNotQueue = 0x4;
inline constexpr std::uint32_t kNamePrimaryOwner = 1;
inline constexpr std::uint32_t kNameInQueue = 2;
inline constexpr std::uint32_t kNameExists = 3;
inline constexpr std::uint32_t kNameAlreadyOwner = 4;

// calls to the bus
Message helloCall();
Message listNamesCall();
Message requestNameCall(std::string name, std::uint32_t flags);
Message nameHasOwnerCall(std::string name);
Message getNameOwnerCall(std::string name);
Message addMatchCall(std::string rule);

// calls to org.freedesktop.DBus.Properties of an object
Message propertyGetCall(std::string destination, std::string path, std::string interface, std::string name);
Message propertyGetAllCall(std::string destination, std::string path, std::string interface);
Message propertySetCall(std::string destination, std::string path, std::string interface, std::string name,
                        Value value);

// match rules for AddMatch; nullopt when a part is not a valid name or path, which also keeps quotes out of the rule
std::optional<std::string> propertiesChangedRule(std::string_view sender, std::string_view path);
std::optional<std::string> nameOwnerChangedRule(std::string_view name);

struct PropertiesChanged {
  // the unique name that sent it and the object it is about
  std::string sender;
  std::string path;
  std::string interface;
  std::vector<std::pair<std::string, Value>> changed;
  std::vector<std::string> invalidated;
};
// nullopt for any other message, or one whose body is not (s, a{sv}, as). whoever sent it: see the one below
std::optional<PropertiesChanged> parsePropertiesChanged(const Message& signal);
// the same, only when it came from sender about path. a signal sent to this connection's own name reaches it whatever
// its match rules, so any peer on the bus can send one; sender is the owner's unique name (from GetNameOwner or
// NameOwnerChanged), and a well-known name, which no message carries as its sender, accepts nothing
std::optional<PropertiesChanged> parsePropertiesChangedFrom(const Message& signal, std::string_view sender,
                                                            std::string_view path);

struct NameOwnerChanged {
  std::string name;
  // empty when there was none, or is none now
  std::string oldOwner;
  std::string newOwner;
};
// nullopt for any other message, or one the bus did not send
std::optional<NameOwnerChanged> parseNameOwnerChanged(const Message& signal);

// an a{sv}'s entries, each value out of its variant; nullopt for any other value
std::optional<std::vector<std::pair<std::string, Value>>> parsePropertyMap(const Value& value);

}  // namespace cadence::dbus

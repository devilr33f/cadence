#include "dbus/message.h"

#include <catch2/catch_test_macros.hpp>

#include <cstddef>
#include <cstdint>
#include <random>
#include <string>
#include <vector>

using namespace cadence::dbus;

namespace {

using Bytes = std::vector<std::uint8_t>;

Bytes text(std::string_view chars) { return {chars.begin(), chars.end()}; }

Bytes concat(std::initializer_list<Bytes> parts) {
  Bytes out;
  for (const auto& part : parts)
    out.insert(out.end(), part.begin(), part.end());
  return out;
}

Bytes encoded(const Message& message) {
  auto result = encodeMessage(message);
  REQUIRE(result.ok());
  return result.value();
}

WireError decodeError(const Bytes& bytes) {
  const auto result = decodeMessage(bytes);
  REQUIRE_FALSE(result.ok());
  return result.error();
}

// METHOD_RETURN serial 7 to serial 3, signature su, body ("ok", 9): hand-computed in both byte orders
const Bytes kReturnLittle{0x6c, 0x02, 0x00, 0x01, 0x0c, 0x00, 0x00, 0x00, 0x07, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00,
                          0x00, 0x05, 0x01, 'u',  0x00, 0x03, 0x00, 0x00, 0x00, 0x08, 0x01, 'g',  0x00, 0x02, 's',
                          'u',  0x00, 0x02, 0x00, 0x00, 0x00, 'o',  'k',  0x00, 0x00, 0x09, 0x00, 0x00, 0x00};
const Bytes kReturnBig{0x42, 0x02, 0x00, 0x01, 0x00, 0x00, 0x00, 0x0c, 0x00, 0x00, 0x00, 0x07, 0x00, 0x00, 0x00,
                       0x10, 0x05, 0x01, 'u',  0x00, 0x00, 0x00, 0x00, 0x03, 0x08, 0x01, 'g',  0x00, 0x02, 's',
                       'u',  0x00, 0x00, 0x00, 0x00, 0x02, 'o',  'k',  0x00, 0x00, 0x00, 0x00, 0x00, 0x09};

Message returnMessage() {
  Message message;
  message.type = MessageType::MethodReturn;
  message.serial = 7;
  message.replySerial = 3;
  message.body = {Value::string("ok"), Value::uint32(9)};
  return message;
}

// a signal with every header field this client sends, and a body
Message richSignal() {
  auto message = Message::signal("/org/gnome/ShairportSync", "org.freedesktop.DBus.Properties", "PropertiesChanged",
                                 {Value::string("org.gnome.ShairportSync.RemoteControl"),
                                  Value::propertyMap({{"PlayerState", Value::string("Playing")}}),
                                  Value::array("s", {Value::string("Metadata")})});
  message.serial = 42;
  message.sender = ":1.7";
  message.destination = ":1.3";
  return message;
}

// a little-endian header of type, serial and fields, padded to 8, then body
Bytes handMade(std::uint8_t type, std::uint32_t serial, const Bytes& fields, const Bytes& body = {}) {
  auto u32 = [](std::uint32_t value) {
    return Bytes{static_cast<std::uint8_t>(value), static_cast<std::uint8_t>(value >> 8),
                 static_cast<std::uint8_t>(value >> 16), static_cast<std::uint8_t>(value >> 24)};
  };
  auto bytes = concat({{'l', type, 0x00, 0x01}, u32(static_cast<std::uint32_t>(body.size())), u32(serial),
                       u32(static_cast<std::uint32_t>(fields.size())), fields});
  while (bytes.size() % 8 != 0)
    bytes.push_back(0);
  bytes.insert(bytes.end(), body.begin(), body.end());
  return bytes;
}

// header fields as their array holds them: each at a multiple of 8, no padding after the last
Bytes fields(std::initializer_list<Bytes> parts) {
  Bytes out;
  for (const auto& part : parts) {
    while (out.size() % 8 != 0)
      out.push_back(0);
    out.insert(out.end(), part.begin(), part.end());
  }
  return out;
}

// a string-typed header field
Bytes stringField(std::uint8_t code, std::string_view value) {
  return concat({{code, 0x01, 's', 0x00, static_cast<std::uint8_t>(value.size()), 0x00, 0x00, 0x00}, text(value),
                 {0x00}});
}

Bytes pathField(std::string_view value) {
  auto field = stringField(1, value);
  field[2] = 'o';
  return field;
}

}  // namespace

TEST_CASE("the Hello call, byte for byte") {
  const auto message = Message::methodCall("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus",
                                           "Hello");
  auto serialized = message;
  serialized.serial = 1;
  // fields: PATH (o), INTERFACE, MEMBER, DESTINATION, each struct at a multiple of 8; their array is 109 bytes long
  const auto expected = concat({{'l', 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x6d, 0x00,
                                 0x00, 0x00},
                                {0x01, 0x01, 'o', 0x00, 0x15, 0x00, 0x00, 0x00},
                                text("/org/freedesktop/DBus"),
                                {0x00, 0x00, 0x00},
                                {0x02, 0x01, 's', 0x00, 0x14, 0x00, 0x00, 0x00},
                                text("org.freedesktop.DBus"),
                                {0x00, 0x00, 0x00, 0x00},
                                {0x03, 0x01, 's', 0x00, 0x05, 0x00, 0x00, 0x00},
                                text("Hello"),
                                {0x00, 0x00, 0x00},
                                {0x06, 0x01, 's', 0x00, 0x14, 0x00, 0x00, 0x00},
                                text("org.freedesktop.DBus"),
                                {0x00, 0x00, 0x00, 0x00}});
  REQUIRE(expected.size() == 128);
  CHECK(encoded(serialized) == expected);
  const auto back = decodeMessage(expected);
  REQUIRE(back.ok());
  CHECK(back.value().member == "Hello");
  CHECK(back.value().destination == "org.freedesktop.DBus");
  CHECK(back.value().serial == 1);
}

TEST_CASE("a reply decodes the same from either byte order, and encodes little-endian") {
  CHECK(encoded(returnMessage()) == kReturnLittle);
  for (const auto& bytes : {kReturnLittle, kReturnBig}) {
    const auto decoded = decodeMessage(bytes);
    REQUIRE(decoded.ok());
    const auto& message = decoded.value();
    CHECK(message.type == MessageType::MethodReturn);
    CHECK(message.serial == 7);
    CHECK(message.replySerial == 3u);
    CHECK(message.signature() == "su");
    CHECK(message.body == returnMessage().body);
  }
}

TEST_CASE("every header field this client sends survives the trip") {
  const auto bytes = encoded(richSignal());
  const auto decoded = decodeMessage(bytes);
  REQUIRE(decoded.ok());
  const auto& message = decoded.value();
  const auto expected = richSignal();
  CHECK(message.type == MessageType::Signal);
  CHECK(message.path == expected.path);
  CHECK(message.interface == expected.interface);
  CHECK(message.member == expected.member);
  CHECK(message.sender == ":1.7");
  CHECK(message.destination == ":1.3");
  CHECK(message.body == expected.body);

  auto error = Message::error(richSignal(), "org.cadence.Test1.Error.Refused", "refused");
  error.serial = 43;
  const auto back = decodeMessage(encoded(error));
  REQUIRE(back.ok());
  CHECK(back.value().type == MessageType::Error);
  CHECK(back.value().errorName == "org.cadence.Test1.Error.Refused");
  CHECK(back.value().replySerial == 42u);
  CHECK(back.value().destination == ":1.7");
  CHECK(back.value().body == std::vector<Value>{Value::string("refused")});
}

TEST_CASE("names follow the specification") {
  CHECK(validInterfaceName("org.gnome.ShairportSync.RemoteControl"));
  CHECK(validInterfaceName("a._1"));
  for (const auto* invalid : {"", "org", "org.", ".org", "org..gnome", "org.1gnome", "org.gno-me"})
    CHECK_FALSE(validInterfaceName(invalid));
  CHECK_FALSE(validInterfaceName(std::string(128, 'a') + "." + std::string(127, 'b')));
  CHECK(validMemberName("PropertiesChanged"));
  for (const auto* invalid : {"", "1st", "a.b", "a-b"})
    CHECK_FALSE(validMemberName(invalid));
  CHECK(validBusName(":1.5"));
  CHECK(validBusName("org.gnome.ShairportSync"));
  CHECK(validBusName("org.cadence-test.A"));
  for (const auto* invalid : {"", ":", "org", ".org.a", "org.1a", "org..a", "org.a b"})
    CHECK_FALSE(validBusName(invalid));
}

TEST_CASE("a message's size from its first 16 bytes") {
  CHECK(messageSize(Bytes(kReturnLittle.begin(), kReturnLittle.begin() + 15)).error() == WireError::Truncated);
  CHECK(messageSize(Bytes(kReturnLittle.begin(), kReturnLittle.begin() + 16)).value() == kReturnLittle.size());
  CHECK(messageSize(kReturnBig).value() == kReturnBig.size());
  auto huge = kReturnLittle;
  huge[6] = 0x10;  // a body of 1 MiB and more
  CHECK(messageSize(huge).error() == WireError::TooBig);
  auto hugeFields = kReturnLittle;
  hugeFields[15] = 0x01;
  CHECK(messageSize(hugeFields).error() == WireError::TooBig);
}

TEST_CASE("the stream is cut into messages as they arrive") {
  MessageReader reader;
  const auto two = concat({kReturnLittle, kReturnBig});
  std::vector<Message> messages;
  for (const auto byte : two) {
    reader.append(std::span(&byte, 1));
    while (auto message = reader.next())
      messages.push_back(std::move(*message));
  }
  CHECK_FALSE(reader.failure());
  REQUIRE(messages.size() == 2);
  CHECK(messages[0].body == messages[1].body);
}

TEST_CASE("the reader is ready with a whole message, or a broken one, and not with a part") {
  MessageReader reader;
  CHECK_FALSE(reader.ready());
  reader.append(std::span(kReturnLittle).first(20));
  CHECK_FALSE(reader.ready());
  reader.append(std::span(kReturnLittle).subspan(20));
  CHECK(reader.ready());
  CHECK(reader.next());
  CHECK_FALSE(reader.ready());

  MessageReader broken;
  auto bytes = kReturnLittle;
  bytes[0] = 'x';
  broken.append(std::span(bytes).first(16));
  CHECK(broken.ready());
  CHECK_FALSE(broken.next());
  CHECK(broken.failure() == WireError::Endianness);
  CHECK_FALSE(broken.ready());
}

TEST_CASE("one over 1 MiB is refused from its first 16 bytes") {
  MessageReader reader;
  auto huge = kReturnLittle;
  huge[6] = 0x10;
  reader.append(std::span(huge).first(16));
  CHECK_FALSE(reader.next());
  CHECK(reader.failure() == WireError::TooBig);
  reader.append(kReturnLittle);
  CHECK_FALSE(reader.next());
}

TEST_CASE("a malformed header is refused with a code") {
  const auto member = stringField(3, "Ping");
  const auto path = pathField("/a");
  const auto callFields = fields({path, member});

  CHECK(decodeMessage(handMade(1, 1, callFields)).ok());
  SECTION("byte order") {
    auto bytes = handMade(1, 1, callFields);
    bytes[0] = 'x';
    CHECK(decodeError(bytes) == WireError::Endianness);
  }
  SECTION("protocol version") {
    auto bytes = handMade(1, 1, callFields);
    bytes[3] = 2;
    CHECK(decodeError(bytes) == WireError::Version);
  }
  SECTION("serial zero") { CHECK(decodeError(handMade(1, 0, callFields)) == WireError::Serial); }
  SECTION("the invalid message type") { CHECK(decodeError(handMade(0, 1, callFields)) == WireError::HeaderField); }
  SECTION("header padding that is not zero") {
    auto bytes = handMade(1, 1, fields({path, stringField(3, "Pin")}));
    bytes.back() = 0x01;
    CHECK(decodeError(bytes) == WireError::Padding);
  }
  SECTION("a call without its member") { CHECK(decodeError(handMade(1, 1, path)) == WireError::HeaderField); }
  SECTION("a signal without its interface") {
    CHECK(decodeError(handMade(4, 1, callFields)) == WireError::HeaderField);
  }
  SECTION("a reply without its reply serial") { CHECK(decodeError(handMade(2, 1, {})) == WireError::HeaderField); }
  SECTION("a known field of the wrong type") {
    const Bytes interfaceAsUint{0x02, 0x01, 'u', 0x00, 0x05, 0x00, 0x00, 0x00};
    CHECK(decodeError(handMade(1, 1, fields({callFields, interfaceAsUint}))) == WireError::HeaderField);
  }
  SECTION("a known field twice") {
    CHECK(decodeError(handMade(1, 1, fields({callFields, member}))) == WireError::HeaderField);
  }
  SECTION("an invalid name") {
    CHECK(decodeError(handMade(1, 1, fields({path, stringField(3, "a.b")}))) == WireError::Name);
    CHECK(decodeError(handMade(1, 1, fields({callFields, stringField(2, "nodots")}))) == WireError::Name);
    CHECK(decodeError(handMade(1, 1, fields({callFields, stringField(7, "bad name")}))) == WireError::Name);
  }
  SECTION("an invalid path") { CHECK(decodeError(handMade(1, 1, fields({pathField("/a/"), member}))) ==
                                     WireError::ObjectPath); }
  SECTION("unix fds") {
    const Bytes fds{0x09, 0x01, 'u', 0x00, 0x01, 0x00, 0x00, 0x00};
    CHECK(decodeError(handMade(1, 1, fields({callFields, fds}))) == WireError::UnixFds);
  }
  SECTION("field code zero") {
    const Bytes invalid{0x00, 0x01, 'u', 0x00, 0x01, 0x00, 0x00, 0x00};
    CHECK(decodeError(handMade(1, 1, fields({callFields, invalid}))) == WireError::HeaderField);
  }
  SECTION("a body without a signature") {
    CHECK(decodeError(handMade(1, 1, callFields, {0x01, 0x00, 0x00, 0x00})) == WireError::Mismatch);
  }
  SECTION("a body longer than its signature") {
    const Bytes signature{0x08, 0x01, 'g', 0x00, 0x01, 'u', 0x00};
    CHECK(decodeError(handMade(1, 1, fields({callFields, signature}), {0x01, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00,
                                                                        0x00})) == WireError::Mismatch);
  }
  SECTION("a body shorter than its signature") {
    const Bytes signature{0x08, 0x01, 'g', 0x00, 0x02, 'u', 'u', 0x00};
    CHECK(decodeError(handMade(1, 1, fields({callFields, signature}), {0x01, 0x00, 0x00, 0x00})) ==
          WireError::Truncated);
  }
  SECTION("bytes past the message") {
    auto bytes = handMade(1, 1, callFields);
    bytes.push_back(0);
    CHECK(decodeError(bytes) == WireError::Mismatch);
  }
}

TEST_CASE("unknown header fields and message types pass, as the specification asks") {
  // code 0x7f, a variant of ay [5, 6]
  const Bytes unknownField{0x7f, 0x02, 'a', 'y', 0x00, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x05, 0x06};
  const auto withUnknown = fields({pathField("/a"), stringField(3, "Ping"), unknownField});
  const auto call = decodeMessage(handMade(1, 1, withUnknown));
  REQUIRE(call.ok());
  CHECK(call.value().member == "Ping");
  const auto unknown = decodeMessage(handMade(9, 1, withUnknown));
  REQUIRE(unknown.ok());
  CHECK(static_cast<int>(unknown.value().type) == 9);
}

TEST_CASE("encoding refuses a message that breaks a rule") {
  auto call = Message::methodCall("org.a.B", "/a", "org.a.B", "Ping");
  CHECK(encodeMessage(call).error() == WireError::Serial);
  call.serial = 1;
  CHECK(encodeMessage(call).ok());
  auto noMember = call;
  noMember.member.clear();
  CHECK(encodeMessage(noMember).error() == WireError::HeaderField);
  auto badMember = call;
  badMember.member = "a.b";
  CHECK(encodeMessage(badMember).error() == WireError::Name);
  auto badPath = call;
  badPath.path = "a";
  CHECK(encodeMessage(badPath).error() == WireError::ObjectPath);
  auto big = call;
  big.body = {Value::bytes(std::vector<std::uint8_t>(kMaxMessage, 0))};
  CHECK(encodeMessage(big).error() == WireError::TooBig);
  auto unknownType = call;
  unknownType.type = static_cast<MessageType>(9);
  CHECK(encodeMessage(unknownType).error() == WireError::HeaderField);
}

TEST_CASE("every truncation is refused and every mutation ends in an error or a message") {
  const auto bytes = encoded(richSignal());
  for (std::size_t size = 0; size < bytes.size(); ++size) {
    const Bytes cut(bytes.begin(), bytes.begin() + static_cast<std::ptrdiff_t>(size));
    CHECK_FALSE(decodeMessage(cut).ok());
    MessageReader reader;
    reader.append(cut);
    CHECK_FALSE(reader.next());
    CHECK_FALSE(reader.failure());
  }
  std::mt19937 random(8112026);
  std::uniform_int_distribution<std::size_t> at(0, bytes.size() - 1);
  std::uniform_int_distribution<int> byte(0, 255);
  for (int round = 0; round < 5000; ++round) {
    auto mutated = bytes;
    for (int flips = 0; flips < 3; ++flips)
      mutated[at(random)] = static_cast<std::uint8_t>(byte(random));
    (void)decodeMessage(mutated);
    MessageReader reader;
    reader.append(mutated);
    while (reader.next())
      ;
  }
}

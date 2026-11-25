#include "dbus/marshal.h"

#include <catch2/catch_test_macros.hpp>

#include <cstddef>
#include <cstdint>
#include <random>
#include <string>
#include <vector>

using namespace cadence::dbus;

namespace {

using Bytes = std::vector<std::uint8_t>;

Bytes encoded(const std::vector<Value>& values) {
  auto result = encodeValues(values);
  if (!result.ok())
    FAIL("encoding refused: " << wireErrorToken(result.error()));
  return result.value();
}

WireError encodeError(const std::vector<Value>& values) {
  const auto result = encodeValues(values);
  REQUIRE_FALSE(result.ok());
  return result.error();
}

std::vector<Value> decoded(const Bytes& bytes, std::string_view signature, ByteOrder order = ByteOrder::Little) {
  auto result = decodeValues(bytes, order, signature);
  if (!result.ok())
    FAIL("decoding refused: " << wireErrorToken(result.error()));
  return result.value();
}

WireError decodeError(const Bytes& bytes, std::string_view signature, ByteOrder order = ByteOrder::Little) {
  const auto result = decodeValues(bytes, order, signature);
  REQUIRE_FALSE(result.ok());
  return result.error();
}

Bytes text(std::string_view chars) { return {chars.begin(), chars.end()}; }

Bytes concat(std::initializer_list<Bytes> parts) {
  Bytes out;
  for (const auto& part : parts)
    out.insert(out.end(), part.begin(), part.end());
  return out;
}

// a body exercising every kind, nested
std::vector<Value> everything() {
  return {
      Value::byte(0xfe),
      Value::boolean(true),
      Value::int16(-2),
      Value::uint16(0xbeef),
      Value::int32(-70000),
      Value::uint32(0xdeadbeef),
      Value::int64(-(std::int64_t{1} << 40)),
      Value::uint64(0x0123456789abcdefULL),
      Value::float64(-30.25),
      Value::string("Cadence, \xd0\x9c\xd0\xb8\xd0\xba\xd1\x83"),
      Value::objectPath("/org/gnome/ShairportSync"),
      Value::signature("a{sv}"),
      Value::bytes({1, 2, 3}),
      Value::array("(yu)", {Value::structure({Value::byte(1), Value::uint32(2)}),
                            Value::structure({Value::byte(3), Value::uint32(4)})}),
      Value::propertyMap({{"PlayerState", Value::string("Playing")},
                          {"AirplayVolume", Value::float64(-15.5)},
                          {"Nested", Value::array("x", {Value::int64(5)})}}),
      Value::variant(Value::variant(Value::array("s", {}))),
  };
}

}  // namespace

TEST_CASE("the specification's string example: foo, + and bar in sequence") {
  const Bytes expected{0x03, 0x00, 0x00, 0x00, 'f', 'o', 'o', 0x00, 0x01, 0x00, 0x00, 0x00,
                       '+',  0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 'b', 'a', 'r', 0x00};
  const std::vector<Value> values{Value::string("foo"), Value::string("+"), Value::string("bar")};
  CHECK(encoded(values) == expected);
  CHECK(decoded(expected, "sss") == values);
}

TEST_CASE("the specification's array of one int64 5, big-endian, and the same little-endian") {
  const Bytes big{0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05};
  const Bytes little{0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00};
  const std::vector<Value> values{Value::array("x", {Value::int64(5)})};
  CHECK(decoded(big, "ax", ByteOrder::Big) == values);
  CHECK(decoded(little, "ax") == values);
  CHECK(encoded(values) == little);
}

TEST_CASE("the specification's variant of a uint64 5, big-endian, and the same little-endian") {
  const Bytes big{0x01, 't', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05};
  const Bytes little{0x01, 't', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00};
  const std::vector<Value> values{Value::variant(Value::uint64(5))};
  CHECK(decoded(big, "v", ByteOrder::Big) == values);
  CHECK(encoded(values) == little);
}

TEST_CASE("a struct aligns to 8 whatever it holds: y then (yu)") {
  const Bytes expected{0x07, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00};
  const std::vector<Value> values{Value::byte(7), Value::structure({Value::byte(1), Value::uint32(2)})};
  CHECK(values[1].type() == "(yu)");
  CHECK(encoded(values) == expected);
  CHECK(decoded(expected, "y(yu)") == values);
}

TEST_CASE("a{sv} with variants: entries aligned to 8, each value after its signature") {
  // length 16 (the entry), padding to 8, then "a", the variant's signature u and the uint32 after its padding
  const Bytes expected{0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
                       'a',  0x00, 0x01, 'u',  0x00, 0x00, 0x00, 0x00, 0x07, 0x00, 0x00, 0x00};
  const std::vector<Value> values{Value::propertyMap({{"a", Value::uint32(7)}})};
  CHECK(values[0].type() == "a{sv}");
  CHECK(encoded(values) == expected);
  CHECK(decoded(expected, "a{sv}") == values);
}

TEST_CASE("nested arrays: aai of [1, 2] and [3]") {
  const Bytes expected{0x14, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
                       0x02, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00};
  const std::vector<Value> values{Value::array(
      "ai", {Value::array("i", {Value::int32(1), Value::int32(2)}), Value::array("i", {Value::int32(3)})})};
  CHECK(encoded(values) == expected);
  CHECK(decoded(expected, "aai") == values);
}

TEST_CASE("an empty array still pads to its element's alignment") {
  const Bytes expected{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00};
  const std::vector<Value> values{Value::array("x", {})};
  CHECK(encoded(values) == expected);
  CHECK(decoded(expected, "ax") == values);
}

TEST_CASE("an array of bytes keeps its bytes together, built either way") {
  const auto built = Value::array("y", {Value::byte(9), Value::byte(8)});
  CHECK(built == Value::bytes({9, 8}));
  REQUIRE(built.asBytes());
  CHECK(built.asBytes()->size() == 2);
  CHECK(built.items().empty());
  CHECK(encoded({built}) == Bytes{0x02, 0x00, 0x00, 0x00, 0x09, 0x08});
}

TEST_CASE("every kind, nested, decodes as it was encoded") {
  const auto values = everything();
  std::string signature;
  for (const auto& value : values)
    signature += value.type();
  CHECK(signature == "ybnqiuxtdsogaya(yu)a{sv}v");
  const auto bytes = encoded(values);
  CHECK(decoded(bytes, signature) == values);
}

TEST_CASE("fixed types big-endian") {
  const Bytes big{0x00, 0x00, 0x00, 0x01, 0xff, 0xfe, 0xbe, 0xef, 0xff, 0xfe, 0xee, 0x90, 0x00, 0x00, 0x00, 0x00,
                  0xc0, 0x3e, 0x40, 0x00, 0x00, 0x00, 0x00, 0x00};
  const std::vector<Value> values{Value::boolean(true), Value::int16(-2), Value::uint16(0xbeef),
                                  Value::int32(-70000), Value::float64(-30.25)};
  CHECK(decoded(big, "bnqid", ByteOrder::Big) == values);
}

TEST_CASE("accessors answer only for their own kind") {
  const auto number = Value::uint32(5);
  CHECK(number.asUint32() == 5u);
  CHECK_FALSE(number.asInt32());
  CHECK(number.asString() == nullptr);
  const auto variant = Value::variant(Value::string("x"));
  REQUIRE(variant.inner() != nullptr);
  CHECK(*variant.inner()->asString() == "x");
  CHECK(Value::objectPath("/a").asString() == nullptr);
  CHECK(*Value::objectPath("/a").asObjectPath() == "/a");
}

TEST_CASE("signatures within the specification's rules") {
  for (const auto& valid :
       std::vector<std::string>{"", "i", "ai", "a{sv}", "(ii)", "a(yv)", "aai", "(i(ii))", "aiai", "a{oa{sa{sv}}}",
                                std::string(32, 'a') + "i", std::string(32, '(') + "i" + std::string(32, ')')}) {
    INFO(valid);
    CHECK(validSignature(valid));
  }
  for (const auto& invalid :
       std::vector<std::string>{"aa", "(ii", "ii)", "()", "{sv}", "a{vs}", "a{(i)s}", "a{s}", "a{sss}", "(i{ss})", "r",
                                "e", "m", "*", "h", "ah", std::string(33, 'a') + "i",
                                std::string(33, '(') + "i" + std::string(33, ')'), std::string(256, 'i'),
                                std::string("i\0", 2)}) {
    INFO(invalid);
    CHECK_FALSE(validSignature(invalid));
  }
  CHECK(validSingleType("a{sv}"));
  CHECK_FALSE(validSingleType("ii"));
  CHECK_FALSE(validSingleType(""));
  CHECK(singleTypeLength("a(ii)ai") == 5);
  CHECK(singleTypeLength("aa{sv}i") == 6);
  CHECK(singleTypeLength("s") == 1);
}

TEST_CASE("utf-8 strictly, and object paths") {
  CHECK(validUtf8("plain"));
  CHECK(validUtf8("\xd0\x9c\xe2\x82\xac\xf0\x9f\x8e\xb5"));
  CHECK(validUtf8("\xef\xbf\xbe"));  // a noncharacter is allowed since specification 0.21
  for (const auto& invalid : {std::string("a\0b", 3), std::string("\xc0\x80"), std::string("\xe0\x80\xaf"),
                              std::string("\xed\xa0\x80"), std::string("\xf4\x90\x80\x80"), std::string("\x80"),
                              std::string("\xe2\x82"), std::string("\xff")}) {
    CHECK_FALSE(validUtf8(invalid));
  }
  CHECK(validObjectPath("/"));
  CHECK(validObjectPath("/org/gnome/ShairportSync"));
  CHECK(validObjectPath("/a_1/B2"));
  for (const auto* invalid : {"", "a", "//", "/a/", "/a//b", "/a-b", "/a.b"})
    CHECK_FALSE(validObjectPath(invalid));
}

TEST_CASE("decoding refuses broken values with a code, never past the bytes") {
  SECTION("padding that is not zero") {
    CHECK(decodeError({0x07, 0x01, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00}, "yu") == WireError::Padding);
  }
  SECTION("a boolean other than 0 or 1") {
    CHECK(decodeError({0x02, 0x00, 0x00, 0x00}, "b") == WireError::Boolean);
  }
  SECTION("a string that is not utf-8, holds a nul or misses its own") {
    CHECK(decodeError({0x02, 0x00, 0x00, 0x00, 0xc0, 0x80, 0x00}, "s") == WireError::Text);
    CHECK(decodeError({0x03, 0x00, 0x00, 0x00, 'a', 0x00, 'b', 0x00}, "s") == WireError::Text);
    CHECK(decodeError({0x01, 0x00, 0x00, 0x00, 'a', 'b'}, "s") == WireError::Text);
  }
  SECTION("an invalid object path") {
    CHECK(decodeError({0x02, 0x00, 0x00, 0x00, '/', '/', 0x00}, "o") == WireError::ObjectPath);
  }
  SECTION("an invalid signature, in a variant too") {
    CHECK(decodeError({0x02, 'a', 'a', 0x00}, "g") == WireError::Signature);
    CHECK(decodeError({0x02, 'i', 'i', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, "v") ==
          WireError::Signature);
  }
  SECTION("an array whose length runs past the body") {
    CHECK(decodeError({0x10, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}, "ai") == WireError::ArrayLength);
  }
  SECTION("array elements that do not end at the array's length") {
    CHECK(decodeError({0x06, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}, "ai") ==
          WireError::ArrayLength);
  }
  SECTION("an array over the specification's 64 MiB") {
    CHECK(decodeError({0x01, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00}, "ay") == WireError::TooBig);
  }
  SECTION("variants nested past 64") {
    Bytes bytes;
    for (int i = 0; i < 64; ++i)
      bytes.insert(bytes.end(), {0x01, 'v', 0x00});
    bytes.insert(bytes.end(), {0x01, 'y', 0x00, 0x05});
    CHECK(decodeError(bytes, "v") == WireError::Depth);
    bytes.erase(bytes.begin(), bytes.begin() + 3);
    CHECK(decoded(bytes, "v").size() == 1);
  }
  SECTION("arrays nested past 32 through a variant, refused from its signature alone") {
    // an array of one variant: its signature (33 bytes), the nul, padding, then an empty array
    const auto type = std::string(32, 'a') + "y";
    const Bytes bytes = concat({{0x28, 0x00, 0x00, 0x00}, {static_cast<std::uint8_t>(type.size())}, text(type),
                                {0x00, 0x00}, {0x00, 0x00, 0x00, 0x00}});
    CHECK(decodeError(bytes, "av") == WireError::Depth);
    CHECK(decoded(Bytes(bytes.begin() + 4, bytes.end()), "v").size() == 1);
  }
  SECTION("a body that does not match its signature") {
    CHECK(decodeError({0x01, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}, "u") == WireError::Mismatch);
    CHECK(decodeError({0x01, 0x00, 0x00, 0x00}, "uu") == WireError::Truncated);
  }
}

TEST_CASE("one message decodes into at most kMaxValues values, an array of bytes counting once") {
  Bytes manyBytes(4 + 100000, 0x00);
  manyBytes[0] = 0xa0;
  manyBytes[1] = 0x86;
  manyBytes[2] = 0x01;
  CHECK(decoded(manyBytes, "ay").size() == 1);

  const std::size_t count = kMaxValues + 1;
  Bytes manyInts(4 + count * 4, 0x00);
  const auto length = static_cast<std::uint32_t>(count * 4);
  for (int i = 0; i < 4; ++i)
    manyInts[static_cast<std::size_t>(i)] = static_cast<std::uint8_t>(length >> (8 * i));
  CHECK(decodeError(manyInts, "ai") == WireError::Values);
}

TEST_CASE("encoding refuses values that break the rules") {
  CHECK(encodeError({Value::array("i", {Value::uint32(1)})}) == WireError::Mismatch);
  CHECK(encodeError({Value::dictEntry(Value::string("k"), Value::byte(1))}) == WireError::Signature);
  CHECK(encodeError({Value::variant(Value::dictEntry(Value::string("k"), Value::byte(1)))}) == WireError::Signature);
  CHECK(encodeError({Value::structure({})}) == WireError::Signature);
  CHECK(encodeError({Value::string(std::string("\xc0\x80"))}) == WireError::Text);
  CHECK(encodeError({Value::objectPath("relative")}) == WireError::ObjectPath);
  CHECK(encodeError({Value::signature("aa")}) == WireError::Signature);
  auto deep = Value::byte(1);
  for (int i = 0; i < 65; ++i)
    deep = Value::variant(std::move(deep));
  CHECK(encodeError({deep}) == WireError::Depth);
}

TEST_CASE("random truncations and flipped bytes end in an error or a value, never past the bytes") {
  const auto values = everything();
  std::string signature;
  for (const auto& value : values)
    signature += value.type();
  const auto bytes = encoded(values);
  for (std::size_t size = 0; size < bytes.size(); ++size) {
    const Bytes cut(bytes.begin(), bytes.begin() + static_cast<std::ptrdiff_t>(size));
    CHECK_FALSE(decodeValues(cut, ByteOrder::Little, signature).ok());
  }
  std::mt19937 random(20261108);
  std::uniform_int_distribution<std::size_t> at(0, bytes.size() - 1);
  std::uniform_int_distribution<int> byte(0, 255);
  for (int round = 0; round < 5000; ++round) {
    auto mutated = bytes;
    for (int flips = 0; flips < 3; ++flips)
      mutated[at(random)] = static_cast<std::uint8_t>(byte(random));
    (void)decodeValues(mutated, ByteOrder::Little, signature);
    (void)decodeValues(mutated, ByteOrder::Big, signature);
  }
}

#pragma once

#include "result.h"

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

// the d-bus type system and wire format, written from the freedesktop.org d-bus specification alone
namespace cadence::dbus {

// our bound on one message, header and body: the specification allows 128 MiB, nothing on the private bus comes near
inline constexpr std::size_t kMaxMessage = std::size_t{1} << 20;
// the specification's bounds
inline constexpr std::size_t kMaxArrayBytes = std::size_t{1} << 26;
inline constexpr std::size_t kMaxSignature = 255;
inline constexpr int kMaxArrayDepth = 32;
inline constexpr int kMaxStructDepth = 32;
inline constexpr int kMaxDepth = 64;
// the values one message decodes into at most, an array of bytes counting once: a 1 MiB body of small values must not
// cost a hundred times that
inline constexpr std::size_t kMaxValues = std::size_t{1} << 16;

enum class ByteOrder : std::uint8_t { Little, Big };

// why bytes were refused
enum class WireError : std::uint8_t {
  Truncated,    // fewer bytes than the lengths say
  TooBig,       // a message over kMaxMessage, an array over the specification's bound
  Endianness,   // neither 'l' nor 'B'
  Version,      // a major protocol version other than 1
  Serial,       // a serial of zero
  Padding,      // alignment padding that is not zero
  Boolean,      // a boolean other than 0 or 1
  Text,         // a string that is not utf-8 or holds a nul
  ObjectPath,   // an invalid object path
  Signature,    // an invalid signature, or one with a type this client refuses (unix fds)
  Depth,        // containers nested past the bounds
  ArrayLength,  // array elements that do not end at the array's length
  Mismatch,     // a value or body that does not match its signature
  Values,       // more than kMaxValues values
  HeaderField,  // a message type or known header field of the wrong kind, or a required field missing
  Name,         // an invalid interface, member, error or bus name
  UnixFds,      // file descriptors, which this client never negotiates
};

// a fixed token for logs
std::string_view wireErrorToken(WireError error);

enum class Kind : std::uint8_t {
  Byte,
  Boolean,
  Int16,
  Uint16,
  Int32,
  Uint32,
  Int64,
  Uint64,
  Double,
  String,
  ObjectPath,
  Signature,
  Array,
  Struct,
  DictEntry,
  Variant,
};

// one value of a single complete type. the factories build containers from their parts; whether the result is valid
// (an array's elements of its element type, a dict entry only inside an array, utf-8 text, the depth bounds) is checked
// when it is encoded. an array of bytes keeps its bytes together (asBytes), not as values (items)
class Value {
 public:
  static Value byte(std::uint8_t value);
  static Value boolean(bool value);
  static Value int16(std::int16_t value);
  static Value uint16(std::uint16_t value);
  static Value int32(std::int32_t value);
  static Value uint32(std::uint32_t value);
  static Value int64(std::int64_t value);
  static Value uint64(std::uint64_t value);
  static Value float64(double value);
  static Value string(std::string text);
  static Value objectPath(std::string path);
  static Value signature(std::string signature);
  static Value bytes(std::vector<std::uint8_t> bytes);
  // elementType is the elements' single complete type, which an empty array still needs
  static Value array(std::string elementType, std::vector<Value> elements);
  static Value structure(std::vector<Value> fields);
  static Value dictEntry(Value key, Value value);
  static Value variant(Value value);
  // a{sv}, the shape of properties: each value wrapped in a variant
  static Value propertyMap(std::vector<std::pair<std::string, Value>> entries);

  [[nodiscard]] Kind kind() const { return kind_; }
  // its single complete type
  [[nodiscard]] std::string_view type() const;

  // nullopt for a value of another kind
  [[nodiscard]] std::optional<std::uint8_t> asByte() const;
  [[nodiscard]] std::optional<bool> asBoolean() const;
  [[nodiscard]] std::optional<std::int16_t> asInt16() const;
  [[nodiscard]] std::optional<std::uint16_t> asUint16() const;
  [[nodiscard]] std::optional<std::int32_t> asInt32() const;
  [[nodiscard]] std::optional<std::uint32_t> asUint32() const;
  [[nodiscard]] std::optional<std::int64_t> asInt64() const;
  [[nodiscard]] std::optional<std::uint64_t> asUint64() const;
  [[nodiscard]] std::optional<double> asDouble() const;
  // nullptr for a value of another kind
  [[nodiscard]] const std::string* asString() const;
  [[nodiscard]] const std::string* asObjectPath() const;
  [[nodiscard]] const std::string* asSignature() const;
  [[nodiscard]] std::optional<std::span<const std::uint8_t>> asBytes() const;
  // a variant's value
  [[nodiscard]] const Value* inner() const;
  // an array's elements (none for an array of bytes), a struct's fields, a dict entry's key and value, a variant's
  // value
  [[nodiscard]] std::span<const Value> items() const { return items_; }

  bool operator==(const Value& other) const = default;

 private:
  explicit Value(Kind kind) : kind_(kind) {}
  [[nodiscard]] bool bytesArray() const;

  Kind kind_;
  // a fixed type's value, a double's bits
  std::uint64_t bits_ = 0;
  // a string-like value's text, an array of bytes' bytes
  std::string text_;
  // a container's type
  std::string type_;
  std::vector<Value> items_;

  friend class Writer;
};

// strict: no overlong form, surrogate or code point above U+10FFFF, and no nul
bool validUtf8(std::string_view text);
bool validObjectPath(std::string_view path);
// zero or more single complete types within the specification's bounds; 'h' (unix fds) is refused
bool validSignature(std::string_view signature);
// exactly one single complete type
bool validSingleType(std::string_view signature);
// the length of the single complete type that starts a valid signature
std::size_t singleTypeLength(std::string_view signature);

// containers entered on the way to a value
struct Depth {
  int arrays = 0;
  int structs = 0;
  int total = 0;
};

// little-endian, each value aligned from the first byte of the buffer
class Writer {
 public:
  // false, with error() saying why, when the value breaks a rule of the specification; the buffer is then not to be
  // used
  bool write(const Value& value);
  // zero bytes up to the next multiple of alignment
  void pad(std::size_t alignment);
  [[nodiscard]] std::size_t size() const { return buffer_.size(); }
  [[nodiscard]] WireError error() const { return error_; }
  std::vector<std::uint8_t> take() { return std::move(buffer_); }

 private:
  bool write(const Value& value, Depth depth);
  bool fail(WireError error);
  void put(std::uint64_t value, std::size_t bytes);
  bool putText(const std::string& text);

  std::vector<std::uint8_t> buffer_;
  WireError error_ = WireError::Mismatch;
};

// reads values from bytes of either byte order, each aligned from the first byte, checking every length, padding and
// text before it is used
class Reader {
 public:
  // budget: the values it may still decode, shared with the caller's other readers of one message
  Reader(std::span<const std::uint8_t> bytes, ByteOrder order, std::size_t& budget);

  // one value of a single complete type the caller validated
  Result<Value, WireError> read(std::string_view type);
  // the values of a valid signature, which must take every byte
  Result<std::vector<Value>, WireError> readAll(std::string_view signature);

 private:
  Result<Value, WireError> read(std::string_view type, Depth depth);
  Result<Value, WireError> readArray(std::string_view elementType, Depth depth);
  Result<Value, WireError> readFields(Kind kind, std::string_view type, Depth depth);
  Result<Value, WireError> readVariant(Depth depth);
  Result<std::string, WireError> readText(std::size_t lengthBytes);
  std::optional<WireError> align(std::size_t alignment);
  std::optional<std::uint64_t> take(std::size_t bytes);
  std::optional<WireError> count();

  std::span<const std::uint8_t> bytes_;
  ByteOrder order_;
  std::size_t& budget_;
  std::size_t position_ = 0;
  // the end of the array being read, else of the bytes: nothing reads past it
  std::size_t limit_;
};

// values as a body is laid out, from an 8-aligned offset
Result<std::vector<std::uint8_t>, WireError> encodeValues(std::span<const Value> values);
// the values of signature, which must take every byte
Result<std::vector<Value>, WireError> decodeValues(std::span<const std::uint8_t> bytes, ByteOrder order,
                                                   std::string_view signature);

}  // namespace cadence::dbus

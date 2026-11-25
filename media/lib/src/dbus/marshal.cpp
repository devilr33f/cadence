#include "dbus/marshal.h"

#include <algorithm>
#include <bit>
#include <utility>

namespace cadence::dbus {

namespace {

// 'h' (unix fds) is left out: this client never passes file descriptors
constexpr std::string_view kBasicTypes = "ybnqiuxtdsog";

bool basicType(char code) { return kBasicTypes.find(code) != std::string_view::npos; }

std::size_t alignmentOf(char code) {
  switch (code) {
    case 'n':
    case 'q':
      return 2;
    case 'b':
    case 'i':
    case 'u':
    case 's':
    case 'o':
    case 'a':
      return 4;
    case 'x':
    case 't':
    case 'd':
    case '(':
    case '{':
      return 8;
    default:
      return 1;
  }
}

// one single complete type at signature[position], with arrays and structs (dict entries counted with them) already
// open around it
bool parseType(std::string_view signature, std::size_t& position, int arrays, int structs) {
  if (position >= signature.size())
    return false;
  const char code = signature[position++];
  if (basicType(code) || code == 'v')
    return true;
  if (code == 'a') {
    if (++arrays > kMaxArrayDepth)
      return false;
    if (position < signature.size() && signature[position] == '{') {
      // a dict entry: only here, a basic key and one value
      if (++structs > kMaxStructDepth)
        return false;
      if (++position >= signature.size() || !basicType(signature[position]))
        return false;
      ++position;
      if (!parseType(signature, position, arrays, structs))
        return false;
      return position < signature.size() && signature[position++] == '}';
    }
    return parseType(signature, position, arrays, structs);
  }
  if (code == '(') {
    if (++structs > kMaxStructDepth)
      return false;
    if (position < signature.size() && signature[position] == ')')
      return false;
    while (position < signature.size() && signature[position] != ')')
      if (!parseType(signature, position, arrays, structs))
        return false;
    return position < signature.size() && signature[position++] == ')';
  }
  return false;
}

// the deepest containers of a valid single complete type, from position
void measure(std::string_view type, std::size_t& position, Depth at, Depth& deepest) {
  const char code = type[position++];
  if (code == 'a') {
    ++at.arrays;
    ++at.total;
    if (type[position] == '{') {
      ++at.structs;
      ++at.total;
      position += 2;
      measure(type, position, at, deepest);
      ++position;
    } else {
      measure(type, position, at, deepest);
    }
  } else if (code == '(') {
    ++at.structs;
    ++at.total;
    while (type[position] != ')')
      measure(type, position, at, deepest);
    ++position;
  }
  deepest.arrays = std::max(deepest.arrays, at.arrays);
  deepest.structs = std::max(deepest.structs, at.structs);
  deepest.total = std::max(deepest.total, at.total);
}

Depth nesting(std::string_view type) {
  Depth deepest;
  std::size_t position = 0;
  measure(type, position, {}, deepest);
  return deepest;
}

// whether a variant of type, inside depth, stays within the bounds: refused from its signature, whatever its value
bool variantFits(Depth depth, std::string_view type) {
  const auto inner = nesting(type);
  return depth.arrays + inner.arrays <= kMaxArrayDepth && depth.structs + inner.structs <= kMaxStructDepth &&
         depth.total + 1 + inner.total <= kMaxDepth;
}

std::size_t roundUp(std::size_t position, std::size_t alignment) {
  return (position + alignment - 1) / alignment * alignment;
}

}  // namespace

std::string_view wireErrorToken(WireError error) {
  switch (error) {
    case WireError::Truncated:
      return "truncated";
    case WireError::TooBig:
      return "too_big";
    case WireError::Endianness:
      return "endianness";
    case WireError::Version:
      return "version";
    case WireError::Serial:
      return "serial";
    case WireError::Padding:
      return "padding";
    case WireError::Boolean:
      return "boolean";
    case WireError::Text:
      return "text";
    case WireError::ObjectPath:
      return "object_path";
    case WireError::Signature:
      return "signature";
    case WireError::Depth:
      return "depth";
    case WireError::ArrayLength:
      return "array_length";
    case WireError::Mismatch:
      return "mismatch";
    case WireError::Values:
      return "values";
    case WireError::HeaderField:
      return "header_field";
    case WireError::Name:
      return "name";
    case WireError::UnixFds:
      return "unix_fds";
  }
  return "mismatch";
}

Value Value::byte(std::uint8_t value) {
  Value out(Kind::Byte);
  out.bits_ = value;
  return out;
}

Value Value::boolean(bool value) {
  Value out(Kind::Boolean);
  out.bits_ = value ? 1 : 0;
  return out;
}

Value Value::int16(std::int16_t value) {
  Value out(Kind::Int16);
  out.bits_ = static_cast<std::uint16_t>(value);
  return out;
}

Value Value::uint16(std::uint16_t value) {
  Value out(Kind::Uint16);
  out.bits_ = value;
  return out;
}

Value Value::int32(std::int32_t value) {
  Value out(Kind::Int32);
  out.bits_ = static_cast<std::uint32_t>(value);
  return out;
}

Value Value::uint32(std::uint32_t value) {
  Value out(Kind::Uint32);
  out.bits_ = value;
  return out;
}

Value Value::int64(std::int64_t value) {
  Value out(Kind::Int64);
  out.bits_ = static_cast<std::uint64_t>(value);
  return out;
}

Value Value::uint64(std::uint64_t value) {
  Value out(Kind::Uint64);
  out.bits_ = value;
  return out;
}

Value Value::float64(double value) {
  Value out(Kind::Double);
  out.bits_ = std::bit_cast<std::uint64_t>(value);
  return out;
}

Value Value::string(std::string text) {
  Value out(Kind::String);
  out.text_ = std::move(text);
  return out;
}

Value Value::objectPath(std::string path) {
  Value out(Kind::ObjectPath);
  out.text_ = std::move(path);
  return out;
}

Value Value::signature(std::string signature) {
  Value out(Kind::Signature);
  out.text_ = std::move(signature);
  return out;
}

Value Value::bytes(std::vector<std::uint8_t> bytes) {
  Value out(Kind::Array);
  out.type_ = "ay";
  out.text_.assign(bytes.begin(), bytes.end());
  return out;
}

Value Value::array(std::string elementType, std::vector<Value> elements) {
  if (elementType == "y" &&
      std::all_of(elements.begin(), elements.end(), [](const Value& element) { return element.kind_ == Kind::Byte; })) {
    std::vector<std::uint8_t> bytes;
    bytes.reserve(elements.size());
    for (const auto& element : elements)
      bytes.push_back(static_cast<std::uint8_t>(element.bits_));
    return Value::bytes(std::move(bytes));
  }
  Value out(Kind::Array);
  out.type_ = "a" + elementType;
  out.items_ = std::move(elements);
  return out;
}

Value Value::structure(std::vector<Value> fields) {
  Value out(Kind::Struct);
  // appended to the empty type, not assigned: gcc 12 (pickle's bookworm builder) falsely flags assigning a one-char
  // literal at -O3 (-Wrestrict)
  out.type_ += '(';
  for (const auto& field : fields)
    out.type_ += field.type();
  out.type_ += ')';
  out.items_ = std::move(fields);
  return out;
}

Value Value::dictEntry(Value key, Value value) {
  Value out(Kind::DictEntry);
  out.type_ += '{';
  out.type_ += key.type();
  out.type_ += value.type();
  out.type_ += '}';
  out.items_.push_back(std::move(key));
  out.items_.push_back(std::move(value));
  return out;
}

Value Value::variant(Value value) {
  Value out(Kind::Variant);
  out.items_.push_back(std::move(value));
  return out;
}

Value Value::propertyMap(std::vector<std::pair<std::string, Value>> entries) {
  std::vector<Value> elements;
  elements.reserve(entries.size());
  for (auto& [name, value] : entries)
    elements.push_back(dictEntry(string(std::move(name)), variant(std::move(value))));
  return array("{sv}", std::move(elements));
}

std::string_view Value::type() const {
  switch (kind_) {
    case Kind::Byte:
      return "y";
    case Kind::Boolean:
      return "b";
    case Kind::Int16:
      return "n";
    case Kind::Uint16:
      return "q";
    case Kind::Int32:
      return "i";
    case Kind::Uint32:
      return "u";
    case Kind::Int64:
      return "x";
    case Kind::Uint64:
      return "t";
    case Kind::Double:
      return "d";
    case Kind::String:
      return "s";
    case Kind::ObjectPath:
      return "o";
    case Kind::Signature:
      return "g";
    case Kind::Variant:
      return "v";
    case Kind::Array:
    case Kind::Struct:
    case Kind::DictEntry:
      break;
  }
  return type_;
}

std::optional<std::uint8_t> Value::asByte() const {
  return kind_ == Kind::Byte ? std::optional(static_cast<std::uint8_t>(bits_)) : std::nullopt;
}

std::optional<bool> Value::asBoolean() const {
  return kind_ == Kind::Boolean ? std::optional(bits_ != 0) : std::nullopt;
}

std::optional<std::int16_t> Value::asInt16() const {
  return kind_ == Kind::Int16 ? std::optional(static_cast<std::int16_t>(static_cast<std::uint16_t>(bits_)))
                              : std::nullopt;
}

std::optional<std::uint16_t> Value::asUint16() const {
  return kind_ == Kind::Uint16 ? std::optional(static_cast<std::uint16_t>(bits_)) : std::nullopt;
}

std::optional<std::int32_t> Value::asInt32() const {
  return kind_ == Kind::Int32 ? std::optional(static_cast<std::int32_t>(static_cast<std::uint32_t>(bits_)))
                              : std::nullopt;
}

std::optional<std::uint32_t> Value::asUint32() const {
  return kind_ == Kind::Uint32 ? std::optional(static_cast<std::uint32_t>(bits_)) : std::nullopt;
}

std::optional<std::int64_t> Value::asInt64() const {
  return kind_ == Kind::Int64 ? std::optional(static_cast<std::int64_t>(bits_)) : std::nullopt;
}

std::optional<std::uint64_t> Value::asUint64() const {
  return kind_ == Kind::Uint64 ? std::optional(bits_) : std::nullopt;
}

std::optional<double> Value::asDouble() const {
  return kind_ == Kind::Double ? std::optional(std::bit_cast<double>(bits_)) : std::nullopt;
}

const std::string* Value::asString() const { return kind_ == Kind::String ? &text_ : nullptr; }

const std::string* Value::asObjectPath() const { return kind_ == Kind::ObjectPath ? &text_ : nullptr; }

const std::string* Value::asSignature() const { return kind_ == Kind::Signature ? &text_ : nullptr; }

std::optional<std::span<const std::uint8_t>> Value::asBytes() const {
  if (!bytesArray())
    return std::nullopt;
  return std::span(reinterpret_cast<const std::uint8_t*>(text_.data()), text_.size());
}

const Value* Value::inner() const { return kind_ == Kind::Variant && !items_.empty() ? items_.data() : nullptr; }

bool Value::bytesArray() const { return kind_ == Kind::Array && type_ == "ay" && items_.empty(); }

bool validUtf8(std::string_view text) {
  static constexpr std::uint32_t kShortest[] = {0, 0, 0x80, 0x800, 0x10000};
  std::size_t at = 0;
  while (at < text.size()) {
    const auto lead = static_cast<unsigned char>(text[at]);
    if (lead == 0)
      return false;
    if (lead < 0x80) {
      ++at;
      continue;
    }
    std::size_t length = 0;
    std::uint32_t point = 0;
    if ((lead & 0xE0) == 0xC0) {
      length = 2;
      point = lead & 0x1Fu;
    } else if ((lead & 0xF0) == 0xE0) {
      length = 3;
      point = lead & 0x0Fu;
    } else if ((lead & 0xF8) == 0xF0) {
      length = 4;
      point = lead & 0x07u;
    } else {
      return false;
    }
    if (text.size() - at < length)
      return false;
    for (std::size_t k = 1; k < length; ++k) {
      const auto next = static_cast<unsigned char>(text[at + k]);
      if ((next & 0xC0) != 0x80)
        return false;
      point = (point << 6) | (next & 0x3Fu);
    }
    if (point < kShortest[length] || point > 0x10FFFF || (point >= 0xD800 && point <= 0xDFFF))
      return false;
    at += length;
  }
  return true;
}

bool validObjectPath(std::string_view path) {
  if (path.empty() || path[0] != '/')
    return false;
  if (path.size() == 1)
    return true;
  if (path.back() == '/')
    return false;
  bool afterSlash = true;
  for (const char c : path.substr(1)) {
    if (c == '/') {
      if (afterSlash)
        return false;
      afterSlash = true;
    } else if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
      afterSlash = false;
    } else {
      return false;
    }
  }
  return true;
}

bool validSignature(std::string_view signature) {
  if (signature.size() > kMaxSignature)
    return false;
  std::size_t position = 0;
  while (position < signature.size())
    if (!parseType(signature, position, 0, 0))
      return false;
  return true;
}

bool validSingleType(std::string_view signature) {
  if (signature.empty() || signature.size() > kMaxSignature)
    return false;
  std::size_t position = 0;
  return parseType(signature, position, 0, 0) && position == signature.size();
}

std::size_t singleTypeLength(std::string_view signature) {
  std::size_t position = 0;
  while (position < signature.size() && signature[position] == 'a')
    ++position;
  if (position >= signature.size())
    return signature.size();
  if (signature[position] != '(' && signature[position] != '{')
    return position + 1;
  int open = 0;
  for (; position < signature.size(); ++position) {
    const char code = signature[position];
    if (code == '(' || code == '{') {
      ++open;
    } else if ((code == ')' || code == '}') && --open == 0) {
      return position + 1;
    }
  }
  return signature.size();
}

bool Writer::write(const Value& value) {
  if (!validSingleType(value.type()))
    return fail(WireError::Signature);
  return write(value, Depth{});
}

void Writer::pad(std::size_t alignment) { buffer_.resize(roundUp(buffer_.size(), alignment), 0); }

bool Writer::fail(WireError error) {
  error_ = error;
  return false;
}

void Writer::put(std::uint64_t value, std::size_t bytes) {
  for (std::size_t i = 0; i < bytes; ++i)
    buffer_.push_back(static_cast<std::uint8_t>(value >> (8 * i)));
}

bool Writer::putText(const std::string& text) {
  if (text.size() > kMaxMessage)
    return fail(WireError::TooBig);
  pad(4);
  put(text.size(), 4);
  buffer_.insert(buffer_.end(), text.begin(), text.end());
  buffer_.push_back(0);
  return true;
}

bool Writer::write(const Value& value, Depth depth) {
  if (buffer_.size() > kMaxMessage)
    return fail(WireError::TooBig);
  switch (value.kind_) {
    case Kind::Byte:
      buffer_.push_back(static_cast<std::uint8_t>(value.bits_));
      return true;
    case Kind::Int16:
    case Kind::Uint16:
      pad(2);
      put(value.bits_, 2);
      return true;
    case Kind::Boolean:
    case Kind::Int32:
    case Kind::Uint32:
      pad(4);
      put(value.bits_, 4);
      return true;
    case Kind::Int64:
    case Kind::Uint64:
    case Kind::Double:
      pad(8);
      put(value.bits_, 8);
      return true;
    case Kind::String:
      return validUtf8(value.text_) ? putText(value.text_) : fail(WireError::Text);
    case Kind::ObjectPath:
      return validObjectPath(value.text_) ? putText(value.text_) : fail(WireError::ObjectPath);
    case Kind::Signature:
      if (!validSignature(value.text_))
        return fail(WireError::Signature);
      buffer_.push_back(static_cast<std::uint8_t>(value.text_.size()));
      buffer_.insert(buffer_.end(), value.text_.begin(), value.text_.end());
      buffer_.push_back(0);
      return true;
    case Kind::Array: {
      if (++depth.arrays > kMaxArrayDepth || ++depth.total > kMaxDepth)
        return fail(WireError::Depth);
      const std::string_view elementType = std::string_view(value.type_).substr(1);
      if (elementType.empty())
        return fail(WireError::Signature);
      pad(4);
      const auto lengthAt = buffer_.size();
      put(0, 4);
      pad(alignmentOf(elementType[0]));
      const auto start = buffer_.size();
      if (value.bytesArray()) {
        buffer_.insert(buffer_.end(), value.text_.begin(), value.text_.end());
      } else {
        for (const auto& element : value.items_) {
          if (element.type() != elementType)
            return fail(WireError::Mismatch);
          if (!write(element, depth))
            return false;
        }
      }
      const auto length = buffer_.size() - start;
      if (length > kMaxArrayBytes || buffer_.size() > kMaxMessage)
        return fail(WireError::TooBig);
      for (std::size_t i = 0; i < 4; ++i)
        buffer_[lengthAt + i] = static_cast<std::uint8_t>(length >> (8 * i));
      return true;
    }
    case Kind::Struct:
    case Kind::DictEntry:
      if (++depth.structs > kMaxStructDepth || ++depth.total > kMaxDepth)
        return fail(WireError::Depth);
      pad(8);
      for (const auto& field : value.items_)
        if (!write(field, depth))
          return false;
      return true;
    case Kind::Variant: {
      if (value.items_.size() != 1)
        return fail(WireError::Mismatch);
      const auto& inner = value.items_.front();
      const auto type = inner.type();
      if (!validSingleType(type))
        return fail(WireError::Signature);
      if (!variantFits(depth, type))
        return fail(WireError::Depth);
      ++depth.total;
      buffer_.push_back(static_cast<std::uint8_t>(type.size()));
      buffer_.insert(buffer_.end(), type.begin(), type.end());
      buffer_.push_back(0);
      return write(inner, depth);
    }
  }
  return fail(WireError::Mismatch);
}

Reader::Reader(std::span<const std::uint8_t> bytes, ByteOrder order, std::size_t& budget)
    : bytes_(bytes), order_(order), budget_(budget), limit_(bytes.size()) {}

Result<Value, WireError> Reader::read(std::string_view type) { return read(type, Depth{}); }

Result<std::vector<Value>, WireError> Reader::readAll(std::string_view signature) {
  std::vector<Value> values;
  for (std::size_t at = 0; at < signature.size();) {
    const auto length = singleTypeLength(signature.substr(at));
    auto value = read(signature.substr(at, length));
    if (!value.ok())
      return value.error();
    values.push_back(std::move(value.value()));
    at += length;
  }
  if (position_ != bytes_.size())
    return WireError::Mismatch;
  return values;
}

std::optional<WireError> Reader::count() {
  if (budget_ == 0)
    return WireError::Values;
  --budget_;
  return std::nullopt;
}

std::optional<WireError> Reader::align(std::size_t alignment) {
  const auto next = roundUp(position_, alignment);
  if (next > limit_)
    return WireError::Truncated;
  for (; position_ < next; ++position_)
    if (bytes_[position_] != 0)
      return WireError::Padding;
  return std::nullopt;
}

std::optional<std::uint64_t> Reader::take(std::size_t bytes) {
  if (bytes > limit_ - position_)
    return std::nullopt;
  std::uint64_t value = 0;
  for (std::size_t i = 0; i < bytes; ++i) {
    const auto byte = bytes_[position_ + (order_ == ByteOrder::Little ? bytes - 1 - i : i)];
    value = (value << 8) | byte;
  }
  position_ += bytes;
  return value;
}

Result<std::string, WireError> Reader::readText(std::size_t lengthBytes) {
  if (const auto error = align(lengthBytes))
    return *error;
  const auto length = take(lengthBytes);
  if (!length)
    return WireError::Truncated;
  // the text and its nul
  if (*length >= limit_ - position_)
    return WireError::Truncated;
  const auto size = static_cast<std::size_t>(*length);
  std::string text(reinterpret_cast<const char*>(bytes_.data() + position_), size);
  if (bytes_[position_ + size] != 0 || !validUtf8(text))
    return WireError::Text;
  position_ += size + 1;
  return text;
}

Result<Value, WireError> Reader::read(std::string_view type, Depth depth) {
  if (const auto error = count())
    return *error;
  const char code = type.empty() ? '\0' : type[0];
  std::size_t size = 0;
  switch (code) {
    case 'y':
      size = 1;
      break;
    case 'n':
    case 'q':
      size = 2;
      break;
    case 'b':
    case 'i':
    case 'u':
      size = 4;
      break;
    case 'x':
    case 't':
    case 'd':
      size = 8;
      break;
    case 's': {
      auto text = readText(4);
      if (!text.ok())
        return text.error();
      return Value::string(std::move(text.value()));
    }
    case 'o': {
      auto text = readText(4);
      if (!text.ok())
        return text.error();
      if (!validObjectPath(text.value()))
        return WireError::ObjectPath;
      return Value::objectPath(std::move(text.value()));
    }
    case 'g': {
      auto text = readText(1);
      if (!text.ok())
        return text.error();
      if (!validSignature(text.value()))
        return WireError::Signature;
      return Value::signature(std::move(text.value()));
    }
    case 'a':
      if (type.size() < 2)
        return WireError::Signature;
      return readArray(type.substr(1), depth);
    case '(':
      return readFields(Kind::Struct, type, depth);
    case '{':
      return readFields(Kind::DictEntry, type, depth);
    case 'v':
      return readVariant(depth);
    default:
      return WireError::Signature;
  }
  if (const auto error = align(size))
    return *error;
  const auto bits = take(size);
  if (!bits)
    return WireError::Truncated;
  switch (code) {
    case 'y':
      return Value::byte(static_cast<std::uint8_t>(*bits));
    case 'b':
      if (*bits > 1)
        return WireError::Boolean;
      return Value::boolean(*bits == 1);
    case 'n':
      return Value::int16(static_cast<std::int16_t>(static_cast<std::uint16_t>(*bits)));
    case 'q':
      return Value::uint16(static_cast<std::uint16_t>(*bits));
    case 'i':
      return Value::int32(static_cast<std::int32_t>(static_cast<std::uint32_t>(*bits)));
    case 'u':
      return Value::uint32(static_cast<std::uint32_t>(*bits));
    case 'x':
      return Value::int64(static_cast<std::int64_t>(*bits));
    case 't':
      return Value::uint64(*bits);
    default:
      return Value::float64(std::bit_cast<double>(*bits));
  }
}

Result<Value, WireError> Reader::readArray(std::string_view elementType, Depth depth) {
  if (++depth.arrays > kMaxArrayDepth || ++depth.total > kMaxDepth)
    return WireError::Depth;
  if (const auto error = align(4))
    return *error;
  const auto length = take(4);
  if (!length)
    return WireError::Truncated;
  if (*length > kMaxArrayBytes)
    return WireError::TooBig;
  // the padding to the first element is there even when there is none
  if (const auto error = align(alignmentOf(elementType[0])))
    return *error;
  if (*length > limit_ - position_)
    return WireError::ArrayLength;
  const auto end = position_ + static_cast<std::size_t>(*length);
  if (elementType == "y") {
    std::vector<std::uint8_t> bytes(bytes_.begin() + static_cast<std::ptrdiff_t>(position_),
                                    bytes_.begin() + static_cast<std::ptrdiff_t>(end));
    position_ = end;
    return Value::bytes(std::move(bytes));
  }
  std::vector<Value> elements;
  const auto outer = std::exchange(limit_, end);
  while (position_ < end) {
    auto element = read(elementType, depth);
    if (!element.ok()) {
      limit_ = outer;
      // an element that runs past the array's end breaks its length, not the body's
      return element.error() == WireError::Truncated ? WireError::ArrayLength : element.error();
    }
    elements.push_back(std::move(element.value()));
  }
  limit_ = outer;
  return Value::array(std::string(elementType), std::move(elements));
}

Result<Value, WireError> Reader::readFields(Kind kind, std::string_view type, Depth depth) {
  if (++depth.structs > kMaxStructDepth || ++depth.total > kMaxDepth)
    return WireError::Depth;
  if (const auto error = align(8))
    return *error;
  const auto inner = type.substr(1, type.size() - 2);
  std::vector<Value> fields;
  for (std::size_t at = 0; at < inner.size();) {
    const auto length = singleTypeLength(inner.substr(at));
    auto field = read(inner.substr(at, length), depth);
    if (!field.ok())
      return field.error();
    fields.push_back(std::move(field.value()));
    at += length;
  }
  if (kind == Kind::DictEntry) {
    if (fields.size() != 2)
      return WireError::Signature;
    return Value::dictEntry(std::move(fields[0]), std::move(fields[1]));
  }
  return Value::structure(std::move(fields));
}

Result<Value, WireError> Reader::readVariant(Depth depth) {
  auto type = readText(1);
  if (!type.ok())
    return type.error();
  if (!validSingleType(type.value()))
    return WireError::Signature;
  if (!variantFits(depth, type.value()))
    return WireError::Depth;
  ++depth.total;
  auto value = read(type.value(), depth);
  if (!value.ok())
    return value.error();
  return Value::variant(std::move(value.value()));
}

Result<std::vector<std::uint8_t>, WireError> encodeValues(std::span<const Value> values) {
  std::string signature;
  for (const auto& value : values)
    signature += value.type();
  if (!validSignature(signature))
    return WireError::Signature;
  Writer writer;
  for (const auto& value : values)
    if (!writer.write(value))
      return writer.error();
  return writer.take();
}

Result<std::vector<Value>, WireError> decodeValues(std::span<const std::uint8_t> bytes, ByteOrder order,
                                                   std::string_view signature) {
  if (!validSignature(signature))
    return WireError::Signature;
  std::size_t budget = kMaxValues;
  Reader reader(bytes, order, budget);
  return reader.readAll(signature);
}

}  // namespace cadence::dbus

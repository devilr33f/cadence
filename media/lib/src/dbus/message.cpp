#include "dbus/message.h"

#include <utility>

namespace cadence::dbus {

namespace {

constexpr std::size_t kMaxName = 255;
constexpr std::size_t kPrefix = 16;

// header field codes
constexpr std::uint8_t kFieldPath = 1;
constexpr std::uint8_t kFieldInterface = 2;
constexpr std::uint8_t kFieldMember = 3;
constexpr std::uint8_t kFieldErrorName = 4;
constexpr std::uint8_t kFieldReplySerial = 5;
constexpr std::uint8_t kFieldDestination = 6;
constexpr std::uint8_t kFieldSender = 7;
constexpr std::uint8_t kFieldSignature = 8;
constexpr std::uint8_t kFieldUnixFds = 9;

bool nameCharacter(char c) {
  return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_';
}

bool digit(char c) { return c >= '0' && c <= '9'; }

// two or more non-empty elements split by '.'
bool dottedName(std::string_view name, bool hyphens, bool leadingDigits) {
  std::size_t elements = 0;
  std::size_t start = 0;
  for (;;) {
    const auto end = name.find('.', start);
    const auto element = name.substr(start, end == std::string_view::npos ? std::string_view::npos : end - start);
    if (element.empty() || (!leadingDigits && digit(element[0])))
      return false;
    for (const char c : element)
      if (!nameCharacter(c) && !(hyphens && c == '-'))
        return false;
    ++elements;
    if (end == std::string_view::npos)
      return elements >= 2;
    start = end + 1;
  }
}

bool knownType(MessageType type) {
  switch (type) {
    case MessageType::MethodCall:
    case MessageType::MethodReturn:
    case MessageType::Error:
    case MessageType::Signal:
      return true;
  }
  return false;
}

// whether the fields a type needs are there
bool complete(const Message& message) {
  switch (message.type) {
    case MessageType::MethodCall:
      return !message.path.empty() && !message.member.empty();
    case MessageType::Signal:
      return !message.path.empty() && !message.interface.empty() && !message.member.empty();
    case MessageType::Error:
      return !message.errorName.empty() && message.replySerial.has_value();
    case MessageType::MethodReturn:
      return message.replySerial.has_value();
  }
  return true;
}

std::uint32_t readUint32(std::span<const std::uint8_t> bytes, std::size_t at, ByteOrder order) {
  std::uint32_t value = 0;
  for (std::size_t i = 0; i < 4; ++i)
    value = (value << 8) | bytes[at + (order == ByteOrder::Little ? 3 - i : i)];
  return value;
}

Value field(std::uint8_t code, Value value) {
  return Value::structure({Value::byte(code), Value::variant(std::move(value))});
}

struct Prefix {
  ByteOrder order;
  std::size_t fieldsEnd;
  std::size_t headerEnd;
  std::size_t size;
};

Result<Prefix, WireError> parsePrefix(std::span<const std::uint8_t> bytes) {
  if (bytes.size() < kPrefix)
    return WireError::Truncated;
  ByteOrder order = ByteOrder::Little;
  if (bytes[0] == 'B')
    order = ByteOrder::Big;
  else if (bytes[0] != 'l')
    return WireError::Endianness;
  if (bytes[3] != 1)
    return WireError::Version;
  // in 64 bits, so two lengths near 4 GiB cannot wrap
  const std::uint64_t body = readUint32(bytes, 4, order);
  const std::uint64_t fields = readUint32(bytes, 12, order);
  const std::uint64_t fieldsEnd = kPrefix + fields;
  const std::uint64_t headerEnd = (fieldsEnd + 7) / 8 * 8;
  if (headerEnd + body > kMaxMessage)
    return WireError::TooBig;
  return Prefix{order, static_cast<std::size_t>(fieldsEnd), static_cast<std::size_t>(headerEnd),
                static_cast<std::size_t>(headerEnd + body)};
}

// one header field into message; seen keeps the known codes already there
std::optional<WireError> takeField(const Value& entry, Message& message, std::uint32_t& seen, std::string& signature) {
  const auto code = entry.items()[0].asByte();
  const auto* value = entry.items()[1].inner();
  if (!code || value == nullptr || *code == 0)
    return WireError::HeaderField;
  if (*code == kFieldUnixFds)
    return WireError::UnixFds;
  if (*code > kFieldSignature)
    return std::nullopt;
  const auto bit = std::uint32_t{1} << *code;
  if ((seen & bit) != 0)
    return WireError::HeaderField;
  seen |= bit;
  if (*code == kFieldPath) {
    if (value->asObjectPath() == nullptr)
      return WireError::HeaderField;
    message.path = *value->asObjectPath();
    return std::nullopt;
  }
  if (*code == kFieldReplySerial) {
    if (!value->asUint32())
      return WireError::HeaderField;
    message.replySerial = *value->asUint32();
    return std::nullopt;
  }
  if (*code == kFieldSignature) {
    if (value->asSignature() == nullptr)
      return WireError::HeaderField;
    signature = *value->asSignature();
    return std::nullopt;
  }
  const auto* text = value->asString();
  if (text == nullptr)
    return WireError::HeaderField;
  switch (*code) {
    case kFieldInterface:
      if (!validInterfaceName(*text))
        return WireError::Name;
      message.interface = *text;
      break;
    case kFieldMember:
      if (!validMemberName(*text))
        return WireError::Name;
      message.member = *text;
      break;
    case kFieldErrorName:
      if (!validInterfaceName(*text))
        return WireError::Name;
      message.errorName = *text;
      break;
    case kFieldDestination:
      if (!validBusName(*text))
        return WireError::Name;
      message.destination = *text;
      break;
    default:
      if (!validBusName(*text))
        return WireError::Name;
      message.sender = *text;
      break;
  }
  return std::nullopt;
}

}  // namespace

std::string Message::signature() const {
  std::string out;
  for (const auto& value : body)
    out += value.type();
  return out;
}

Message Message::methodCall(std::string destination, std::string path, std::string interface, std::string member,
                            std::vector<Value> body) {
  Message message;
  message.type = MessageType::MethodCall;
  message.destination = std::move(destination);
  message.path = std::move(path);
  message.interface = std::move(interface);
  message.member = std::move(member);
  message.body = std::move(body);
  return message;
}

Message Message::signal(std::string path, std::string interface, std::string member, std::vector<Value> body) {
  Message message;
  message.type = MessageType::Signal;
  message.path = std::move(path);
  message.interface = std::move(interface);
  message.member = std::move(member);
  message.body = std::move(body);
  return message;
}

Message Message::methodReturn(const Message& call, std::vector<Value> body) {
  Message message;
  message.type = MessageType::MethodReturn;
  message.replySerial = call.serial;
  message.destination = call.sender;
  message.body = std::move(body);
  return message;
}

Message Message::error(const Message& call, std::string name, std::string text) {
  Message message;
  message.type = MessageType::Error;
  message.errorName = std::move(name);
  message.replySerial = call.serial;
  message.destination = call.sender;
  message.body.push_back(Value::string(std::move(text)));
  return message;
}

bool validInterfaceName(std::string_view name) { return name.size() <= kMaxName && dottedName(name, false, false); }

bool validMemberName(std::string_view name) {
  if (name.empty() || name.size() > kMaxName || digit(name[0]))
    return false;
  for (const char c : name)
    if (!nameCharacter(c))
      return false;
  return true;
}

bool validBusName(std::string_view name) {
  if (name.size() > kMaxName)
    return false;
  if (!name.empty() && name[0] == ':')
    return dottedName(name.substr(1), true, true);
  return dottedName(name, true, false);
}

Result<std::vector<std::uint8_t>, WireError> encodeMessage(const Message& message) {
  if (message.serial == 0)
    return WireError::Serial;
  if (!knownType(message.type) || !complete(message))
    return WireError::HeaderField;
  if (!message.path.empty() && !validObjectPath(message.path))
    return WireError::ObjectPath;
  if ((!message.interface.empty() && !validInterfaceName(message.interface)) ||
      (!message.member.empty() && !validMemberName(message.member)) ||
      (!message.errorName.empty() && !validInterfaceName(message.errorName)) ||
      (!message.destination.empty() && !validBusName(message.destination)) ||
      (!message.sender.empty() && !validBusName(message.sender)))
    return WireError::Name;
  const auto signature = message.signature();
  if (!validSignature(signature))
    return WireError::Signature;

  Writer body;
  for (const auto& value : message.body)
    if (!body.write(value))
      return body.error();
  if (body.size() > kMaxMessage)
    return WireError::TooBig;

  std::vector<Value> fields;
  if (!message.path.empty())
    fields.push_back(field(kFieldPath, Value::objectPath(message.path)));
  if (!message.interface.empty())
    fields.push_back(field(kFieldInterface, Value::string(message.interface)));
  if (!message.member.empty())
    fields.push_back(field(kFieldMember, Value::string(message.member)));
  if (!message.errorName.empty())
    fields.push_back(field(kFieldErrorName, Value::string(message.errorName)));
  if (message.replySerial)
    fields.push_back(field(kFieldReplySerial, Value::uint32(*message.replySerial)));
  if (!message.destination.empty())
    fields.push_back(field(kFieldDestination, Value::string(message.destination)));
  if (!message.sender.empty())
    fields.push_back(field(kFieldSender, Value::string(message.sender)));
  if (!signature.empty())
    fields.push_back(field(kFieldSignature, Value::signature(signature)));

  Writer header;
  const bool written = header.write(Value::byte('l')) &&
                       header.write(Value::byte(static_cast<std::uint8_t>(message.type))) &&
                       header.write(Value::byte(message.flags)) && header.write(Value::byte(1)) &&
                       header.write(Value::uint32(static_cast<std::uint32_t>(body.size()))) &&
                       header.write(Value::uint32(message.serial)) &&
                       header.write(Value::array("(yv)", std::move(fields)));
  if (!written)
    return header.error();
  header.pad(8);
  if (header.size() + body.size() > kMaxMessage)
    return WireError::TooBig;
  auto bytes = header.take();
  const auto bodyBytes = body.take();
  bytes.insert(bytes.end(), bodyBytes.begin(), bodyBytes.end());
  return bytes;
}

Result<std::size_t, WireError> messageSize(std::span<const std::uint8_t> bytes) {
  const auto prefix = parsePrefix(bytes);
  if (!prefix.ok())
    return prefix.error();
  return prefix.value().size;
}

Result<Message, WireError> decodeMessage(std::span<const std::uint8_t> bytes) {
  const auto parsed = parsePrefix(bytes);
  if (!parsed.ok())
    return parsed.error();
  const auto& prefix = parsed.value();
  if (bytes.size() < prefix.size)
    return WireError::Truncated;
  if (bytes.size() > prefix.size)
    return WireError::Mismatch;

  std::size_t budget = kMaxValues;
  Reader headerReader(bytes.first(prefix.fieldsEnd), prefix.order, budget);
  auto header = headerReader.readAll("yyyyuua(yv)");
  if (!header.ok())
    return header.error();
  for (std::size_t at = prefix.fieldsEnd; at < prefix.headerEnd; ++at)
    if (bytes[at] != 0)
      return WireError::Padding;

  const auto& values = header.value();
  Message message;
  message.type = static_cast<MessageType>(*values[1].asByte());
  message.flags = *values[2].asByte();
  message.serial = *values[5].asUint32();
  if (message.serial == 0)
    return WireError::Serial;
  if (static_cast<std::uint8_t>(message.type) == 0)
    return WireError::HeaderField;
  std::uint32_t seen = 0;
  std::string signature;
  for (const auto& entry : values[6].items())
    if (const auto error = takeField(entry, message, seen, signature))
      return *error;
  if (!complete(message))
    return WireError::HeaderField;

  Reader bodyReader(bytes.subspan(prefix.headerEnd), prefix.order, budget);
  auto body = bodyReader.readAll(signature);
  if (!body.ok())
    return body.error();
  message.body = std::move(body.value());
  return message;
}

void MessageReader::append(std::span<const std::uint8_t> bytes) {
  if (failure_)
    return;
  // what was read is dropped once it is most of the buffer
  if (offset_ > 0 && offset_ * 2 >= buffer_.size()) {
    buffer_.erase(buffer_.begin(), buffer_.begin() + static_cast<std::ptrdiff_t>(offset_));
    offset_ = 0;
  }
  buffer_.insert(buffer_.end(), bytes.begin(), bytes.end());
}

bool MessageReader::ready() const {
  if (failure_)
    return false;
  const auto unread = std::span<const std::uint8_t>(buffer_).subspan(offset_);
  const auto size = messageSize(unread);
  if (!size.ok())
    return size.error() != WireError::Truncated;
  return unread.size() >= size.value();
}

std::optional<Message> MessageReader::next() {
  if (failure_)
    return std::nullopt;
  const auto unread = std::span<const std::uint8_t>(buffer_).subspan(offset_);
  const auto size = messageSize(unread);
  if (!size.ok()) {
    if (size.error() != WireError::Truncated)
      failure_ = size.error();
    return std::nullopt;
  }
  if (unread.size() < size.value())
    return std::nullopt;
  auto message = decodeMessage(unread.first(size.value()));
  if (!message.ok()) {
    failure_ = message.error();
    return std::nullopt;
  }
  offset_ += size.value();
  if (offset_ == buffer_.size()) {
    buffer_.clear();
    offset_ = 0;
  }
  return std::move(message.value());
}

}  // namespace cadence::dbus

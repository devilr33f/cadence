#pragma once

#include <concepts>
#include <cstdint>
#include <string_view>
#include <variant>

namespace cadence::i18n {

// a number argument: any integer or floating type, kept as int64 or double
class Num {
 public:
  template <std::integral T>
  Num(T v) : v_(static_cast<std::int64_t>(v)) {}  // NOLINT(google-explicit-constructor): call sites read as numbers
  template <std::floating_point T>
  Num(T v) : v_(static_cast<double>(v)) {}  // NOLINT(google-explicit-constructor)

  [[nodiscard]] const std::variant<std::int64_t, double>& get() const { return v_; }

 private:
  std::variant<std::int64_t, double> v_;
};

// an argument's value: text for {name} and select, a number for {name, number} and plural. a view: the caller keeps
// the text alive until the message is formatted
class Value {
 public:
  Value() = default;
  explicit Value(std::string_view s) : v_(s) {}
  explicit Value(Num n) : v_(std::monostate{}) {
    std::visit([this](auto x) { v_ = x; }, n.get());
  }
  explicit Value(std::int64_t n) : v_(n) {}
  explicit Value(double d) : v_(d) {}

  [[nodiscard]] bool empty() const { return std::holds_alternative<std::monostate>(v_); }
  [[nodiscard]] const std::string_view* text() const { return std::get_if<std::string_view>(&v_); }
  [[nodiscard]] bool number() const {
    return std::holds_alternative<std::int64_t>(v_) || std::holds_alternative<double>(v_);
  }
  [[nodiscard]] const std::variant<std::monostate, std::string_view, std::int64_t, double>& get() const { return v_; }

 private:
  std::variant<std::monostate, std::string_view, std::int64_t, double> v_;
};

struct Arg {
  std::string_view name;
  Value value;
};

}  // namespace cadence::i18n

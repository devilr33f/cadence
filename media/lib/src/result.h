#pragma once

#include <utility>
#include <variant>

namespace cadence {

template <typename T, typename E>
class Result {
 public:
  Result(T value) : value_(std::in_place_index<0>, std::move(value)) {}
  Result(E error) : value_(std::in_place_index<1>, std::move(error)) {}

  [[nodiscard]] bool ok() const noexcept { return value_.index() == 0; }
  [[nodiscard]] const T& value() const { return std::get<0>(value_); }
  [[nodiscard]] const E& error() const { return std::get<1>(value_); }
  // for moving a move-only value out
  [[nodiscard]] T& value() { return std::get<0>(value_); }

 private:
  std::variant<T, E> value_;
};

}  // namespace cadence

#pragma once

#include "i18n/lang.h"
#include "i18n/value.h"

#include <cstddef>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::i18n {

enum class ArgType { String, Number };

struct ArgInfo {
  std::string name;
  ArgType type;
};

// a parsed message: the icu messageformat subset pkg/i18n parses, with the same results (i18n/testdata/format.json).
//   text   literal; '' is one apostrophe, and '{', '}' or (inside a plural) '#' quote until the next lone '
//   {name}  {name, number}  {name, plural, =0 {...} one {...} other {...}}  {name, select, a {...} other {...}}
class Message {
 public:
  struct ParseError {
    std::size_t offset;
    std::string reason;
  };

  static std::optional<Message> parse(std::string_view s, ParseError* error = nullptr);

  // a missing or mistyped argument renders as {name} and sets *ok to false; the text is whole otherwise
  [[nodiscard]] std::string format(Lang l, std::span<const Arg> args, bool* ok = nullptr) const;
  // the arguments in the order they first appear, cases included; nullopt when a name is both text and number
  [[nodiscard]] std::optional<std::vector<ArgInfo>> args() const;
  // xx's form of en's message: letters accented, wrapped in [ ] and padded with · to about 40 % longer
  [[nodiscard]] Message pseudolocalized() const;

 private:
  enum class Kind { Text, Arg, Number, Plural, Select, Pound };
  struct Branch;
  struct Node {
    Kind kind = Kind::Text;
    std::string text;
    std::string name;
    std::vector<Branch> cases;
  };
  struct Branch {
    std::string key;
    std::vector<Node> nodes;
  };
  friend class Parser;

  static void format_nodes(const std::vector<Node>& nodes, std::string& out, Lang l, std::span<const Arg> args,
                           std::string_view pound, bool& ok);
  static bool collect(const std::vector<Node>& nodes, std::vector<ArgInfo>& out);
  static std::vector<Node> accent(const std::vector<Node>& nodes);
  static std::size_t literal_runes(const std::vector<Node>& nodes);
  static const std::vector<Node>& pick(const std::vector<Branch>& cases, std::string_view key);

  std::vector<Node> nodes_;
};

}  // namespace cadence::i18n

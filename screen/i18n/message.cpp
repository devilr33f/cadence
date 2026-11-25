#include "i18n/message.h"

#include "i18n/locale.h"

#include <algorithm>
#include <array>
#include <charconv>
#include <utility>

namespace cadence::i18n {

class Parser {
 public:
  explicit Parser(std::string_view s) : s_(s) {}

  bool run(std::vector<Message::Node>& out, Message::ParseError& err) {
    if (!message(out, false)) {
      err = {i_, reason_};
      return false;
    }
    if (i_ < s_.size()) {
      err = {i_, "unbalanced '}'"};
      return false;
    }
    return true;
  }

 private:
  using Node = Message::Node;
  using Kind = Message::Kind;

  bool fail(std::string reason) {
    reason_ = std::move(reason);
    return false;
  }

  // up to an unmatched '}' (left unread) or the end
  bool message(std::vector<Node>& out, bool in_plural) {
    std::string text;
    auto flush = [&] {
      if (!text.empty()) {
        out.push_back(Node{Kind::Text, std::move(text), {}, {}});
        text.clear();
      }
    };
    while (i_ < s_.size()) {
      const char c = s_[i_];
      if (c == '\'') {
        quoted(text, in_plural);
      } else if (c == '{') {
        flush();
        Node n;
        if (!argument(n)) return false;
        out.push_back(std::move(n));
      } else if (c == '}') {
        break;
      } else if (c == '#' && in_plural) {
        flush();
        out.push_back(Node{Kind::Pound, {}, {}, {}});
        ++i_;
      } else {
        text.push_back(c);
        ++i_;
      }
    }
    flush();
    return true;
  }

  bool at(char c) const { return i_ < s_.size() && s_[i_] == c; }
  bool next_is(char c) const { return i_ + 1 < s_.size() && s_[i_ + 1] == c; }

  void quoted(std::string& text, bool in_plural) {
    if (next_is('\'')) {
      text.push_back('\'');
      i_ += 2;
      return;
    }
    if (next_is('{') || next_is('}') || (in_plural && next_is('#'))) {
      ++i_;
      while (i_ < s_.size()) {
        if (s_[i_] == '\'') {
          if (next_is('\'')) {
            text.push_back('\'');
            i_ += 2;
            continue;
          }
          ++i_;
          return;
        }
        text.push_back(s_[i_++]);
      }
      return;
    }
    text.push_back('\'');
    ++i_;
  }

  void space() {
    while (i_ < s_.size() && (s_[i_] == ' ' || s_[i_] == '\t' || s_[i_] == '\n' || s_[i_] == '\r')) ++i_;
  }

  std::string_view word() {
    const std::size_t start = i_;
    while (i_ < s_.size()) {
      const char c = s_[i_];
      if (c == '_' || c == '=' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
          (c >= '0' && c <= '9')) {
        ++i_;
        continue;
      }
      break;
    }
    return s_.substr(start, i_ - start);
  }

  static bool valid_name(std::string_view s) {
    if (s.empty() || (s[0] >= '0' && s[0] <= '9')) return false;
    return std::all_of(s.begin(), s.end(), [](char c) {
      return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9');
    });
  }

  bool argument(Node& n) {
    ++i_;
    space();
    const std::string_view name = word();
    if (!valid_name(name)) return fail("bad argument name");
    n.name = std::string(name);
    space();
    if (i_ >= s_.size()) return fail("unclosed argument");
    if (at('}')) {
      ++i_;
      n.kind = Kind::Arg;
      return true;
    }
    if (!at(',')) return fail("expected ',' or '}'");
    ++i_;
    space();
    const std::string_view type = word();
    space();
    if (type == "number") {
      if (!at('}')) return fail("number takes no style");
      ++i_;
      n.kind = Kind::Number;
      return true;
    }
    if (type == "plural" || type == "select") {
      if (!at(',')) return fail("expected ',' after " + std::string(type));
      ++i_;
      n.kind = type == "plural" ? Kind::Plural : Kind::Select;
      return branches(n);
    }
    return fail("unknown argument type " + std::string(type));
  }

  bool branches(Node& n) {
    const bool plural = n.kind == Kind::Plural;
    bool other = false;
    for (;;) {
      space();
      if (i_ >= s_.size()) return fail(plural ? "unclosed plural" : "unclosed select");
      if (at('}')) {
        ++i_;
        break;
      }
      const std::string_view key = word();
      if (key.empty()) return fail("expected a case key");
      if (!check_key(plural, key)) return false;
      for (const auto& b : n.cases) {
        if (b.key == key) return fail("duplicate case " + std::string(key));
      }
      other = other || key == "other";
      space();
      if (!at('{')) return fail("expected '{' after " + std::string(key));
      ++i_;
      Message::Branch b{std::string(key), {}};
      if (!message(b.nodes, plural)) return false;
      if (i_ >= s_.size()) return fail("unclosed case " + std::string(key));
      ++i_;
      n.cases.push_back(std::move(b));
    }
    if (!other) return fail(plural ? "plural needs other" : "select needs other");
    return true;
  }

  bool check_key(bool plural, std::string_view key) {
    if (!plural) return valid_name(key) || fail("bad select key");
    if (key.front() == '=') {
      const std::string_view digits = key.substr(1);
      std::uint64_t v = 0;
      const auto [end, ec] = std::from_chars(digits.data(), digits.data() + digits.size(), v);
      if (digits.empty() || ec != std::errc{} || end != digits.data() + digits.size() || v > INT64_MAX) {
        return fail("bad explicit case");
      }
      return true;
    }
    static constexpr std::array<std::string_view, 6> kCats{"zero", "one", "two", "few", "many", "other"};
    if (std::find(kCats.begin(), kCats.end(), key) != kCats.end()) return true;
    return fail("unknown plural category");
  }

  std::string_view s_;
  std::size_t i_ = 0;
  std::string reason_;
};

std::optional<Message> Message::parse(std::string_view s, ParseError* error) {
  Message m;
  ParseError err;
  if (!Parser(s).run(m.nodes_, err)) {
    if (error) *error = err;
    return std::nullopt;
  }
  return m;
}

namespace {

const Value* find_arg(std::span<const Arg> args, std::string_view name) {
  for (const auto& a : args) {
    if (a.name == name) return &a.value;
  }
  return nullptr;
}

// the plain digits of a number value, or nullopt for text or none
std::optional<std::string> plain_of(const Value* v) {
  if (!v) return std::nullopt;
  if (const auto* i = std::get_if<std::int64_t>(&v->get())) return plain_number(*i);
  if (const auto* d = std::get_if<double>(&v->get())) return plain_number(*d);
  return std::nullopt;
}

}  // namespace

const std::vector<Message::Node>& Message::pick(const std::vector<Branch>& cases, std::string_view key) {
  static const std::vector<Node> kEmpty;
  const std::vector<Node>* other = &kEmpty;
  for (const auto& c : cases) {
    if (c.key == key) return c.nodes;
    if (c.key == "other") other = &c.nodes;
  }
  return *other;
}

void Message::format_nodes(const std::vector<Node>& nodes, std::string& out, Lang l, std::span<const Arg> args,
                           std::string_view pound, bool& ok) {
  for (const auto& n : nodes) {
    switch (n.kind) {
      case Kind::Text:
        out += n.text;
        break;
      case Kind::Pound:
        out += pound;
        break;
      case Kind::Arg: {
        const Value* v = find_arg(args, n.name);
        if (v && v->text()) {
          out += *v->text();
        } else if (auto plain = plain_of(v)) {
          out += localize_number(l, *plain);
        } else {
          ok = false;
          out += "{" + n.name + "}";
        }
        break;
      }
      case Kind::Number: {
        if (auto plain = plain_of(find_arg(args, n.name))) {
          out += localize_number(l, *plain);
        } else {
          ok = false;
          out += "{" + n.name + "}";
        }
        break;
      }
      case Kind::Select: {
        const Value* v = find_arg(args, n.name);
        std::string_view key;
        if (v && v->text()) {
          key = *v->text();
        } else {
          ok = false;
        }
        format_nodes(pick(n.cases, key), out, l, args, pound, ok);
        break;
      }
      case Kind::Plural: {
        auto plain = plain_of(find_arg(args, n.name));
        if (!plain) {
          ok = false;
          plain = "0";
        }
        const std::string shown = localize_number(l, *plain);
        const std::vector<Node>* chosen = nullptr;
        const std::string exact = "=" + *plain;
        for (const auto& c : n.cases) {
          if (c.key == exact) {
            chosen = &c.nodes;
            break;
          }
        }
        if (!chosen) chosen = &pick(n.cases, plural_name(plural_of(l, *plain)));
        format_nodes(*chosen, out, l, args, shown, ok);
        break;
      }
    }
  }
}

std::string Message::format(Lang l, std::span<const Arg> args, bool* ok) const {
  std::string out;
  bool good = true;
  format_nodes(nodes_, out, l, args, {}, good);
  if (ok) *ok = good;
  return out;
}

bool Message::collect(const std::vector<Node>& nodes, std::vector<ArgInfo>& out) {
  for (const auto& n : nodes) {
    ArgType t;
    switch (n.kind) {
      case Kind::Arg:
      case Kind::Select:
        t = ArgType::String;
        break;
      case Kind::Number:
      case Kind::Plural:
        t = ArgType::Number;
        break;
      default:
        continue;
    }
    auto it = std::find_if(out.begin(), out.end(), [&](const ArgInfo& a) { return a.name == n.name; });
    if (it == out.end()) {
      out.push_back({n.name, t});
    } else if (it->type != t) {
      return false;
    }
    for (const auto& b : n.cases) {
      if (!collect(b.nodes, out)) return false;
    }
  }
  return true;
}

std::optional<std::vector<ArgInfo>> Message::args() const {
  std::vector<ArgInfo> out;
  if (!collect(nodes_, out)) return std::nullopt;
  return out;
}

namespace {

// the pseudo-locale's letters for a-z and A-Z (pkg/i18n has the same table)
constexpr std::array<std::string_view, 26> kPseudoLower{"á", "ƀ", "ç", "ð", "é", "ƒ", "ĝ", "ĥ", "í", "ĵ", "ķ", "ĺ", "ɱ",
                                                        "ñ", "ó", "þ", "ǫ", "ŕ", "š", "ţ", "ú", "ṽ", "ŵ", "ẋ", "ý", "ž"};
constexpr std::array<std::string_view, 26> kPseudoUpper{"Á", "Ɓ", "Ç", "Ð", "É", "Ƒ", "Ĝ", "Ĥ", "Í", "Ĵ", "Ķ", "Ĺ", "Ṁ",
                                                        "Ñ", "Ó", "Þ", "Ǫ", "Ŕ", "Š", "Ţ", "Ú", "Ṽ", "Ŵ", "Ẋ", "Ý", "Ž"};

std::size_t runes(std::string_view s) {
  return static_cast<std::size_t>(
      std::count_if(s.begin(), s.end(), [](char c) { return (static_cast<unsigned char>(c) & 0xc0) != 0x80; }));
}

}  // namespace

std::vector<Message::Node> Message::accent(const std::vector<Node>& nodes) {
  std::vector<Node> out;
  out.reserve(nodes.size());
  for (const auto& n : nodes) {
    Node c{n.kind, {}, n.name, {}};
    if (n.kind == Kind::Text) {
      for (char ch : n.text) {
        if (ch >= 'a' && ch <= 'z') {
          c.text += kPseudoLower[static_cast<std::size_t>(ch - 'a')];
        } else if (ch >= 'A' && ch <= 'Z') {
          c.text += kPseudoUpper[static_cast<std::size_t>(ch - 'A')];
        } else {
          c.text.push_back(ch);
        }
      }
    }
    for (const auto& b : n.cases) c.cases.push_back(Branch{b.key, accent(b.nodes)});
    out.push_back(std::move(c));
  }
  return out;
}

std::size_t Message::literal_runes(const std::vector<Node>& nodes) {
  std::size_t total = 0;
  for (const auto& n : nodes) {
    if (n.kind == Kind::Text) {
      total += runes(n.text);
    } else if (n.kind == Kind::Plural || n.kind == Kind::Select) {
      std::size_t longest = 0;
      for (const auto& b : n.cases) longest = std::max(longest, literal_runes(b.nodes));
      total += longest;
    }
  }
  return total;
}

Message Message::pseudolocalized() const {
  const std::size_t n = literal_runes(nodes_);
  if (n == 0) return *this;
  const std::size_t pad = (n * 2 + 4) / 5;
  Message m;
  m.nodes_.push_back(Node{Kind::Text, "[", {}, {}});
  for (auto& node : accent(nodes_)) m.nodes_.push_back(std::move(node));
  std::string tail;
  for (std::size_t i = 2; i < pad; ++i) tail += "·";
  tail += "]";
  m.nodes_.push_back(Node{Kind::Text, std::move(tail), {}, {}});
  return m;
}

}  // namespace cadence::i18n

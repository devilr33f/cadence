#include "i18n/translator.h"

#include <algorithm>
#include <charconv>
#include <utility>

namespace cadence::i18n {

namespace {

std::size_t slot(Lang l) {
  switch (l) {
    case Lang::Ru:
      return 1;
    case Lang::Pseudo:
      return 2;
    case Lang::En:
      break;
  }
  return 0;
}

// an rml argument's value: a number when the key's argument is one and it parses as an integer or a decimal, else
// the text
Value arg_value(std::string_view s, bool number) {
  if (number) {
    std::int64_t i = 0;
    if (auto [end, ec] = std::from_chars(s.data(), s.data() + s.size(), i);
        ec == std::errc{} && end == s.data() + s.size()) {
      return Value(i);
    }
    double d = 0;
    if (auto [end, ec] = std::from_chars(s.data(), s.data() + s.size(), d);
        ec == std::errc{} && end == s.data() + s.size()) {
      return Value(d);
    }
  }
  return Value(s);
}

}  // namespace

Translator::Translator(Lang lang, std::span<const CatalogEntry> entries) : lang_(lang), entries_(entries) {
  for (auto& c : cache_) c.resize(entries_.size());
}

void Translator::set_language(Lang lang) {
  if (lang == lang_) return;
  lang_ = lang;
  ++generation_;
  for (const auto& l : listeners_) l(lang_);
}

void Translator::on_change(std::function<void(Lang)> listener) { listeners_.push_back(std::move(listener)); }

const Translator::Parsed& Translator::parsed(std::size_t index, Lang lang) {
  Parsed& p = cache_[slot(lang)][index];
  if (p.message) return p;
  const CatalogEntry& e = entries_[index];
  auto m = Message::parse(lang == Lang::Ru ? e.ru : e.en);
  if (!m) {
    // i18ngen refuses a catalog with a broken message, so this is a hand-built table: show the key
    m = Message::parse("'{'" + std::string(e.key) + "'}'");
  }
  if (lang == Lang::Pseudo) m = m->pseudolocalized();
  p.args = m->args().value_or(std::vector<ArgInfo>{});
  p.message = std::make_unique<Message>(std::move(*m));
  return p;
}

std::string Translator::render(std::size_t index, std::span<const Arg> args) {
  if (index >= entries_.size()) return {};
  return parsed(index, lang_).message->format(lang_, args);
}

std::string Translator::tr(std::string_view key, std::span<const Arg> args) {
  const auto index = find_key(entries_, key);
  if (!index) return std::string(key);
  return render(*index, args);
}

std::optional<std::string> Translator::expand(std::string_view inner) {
  const std::size_t bar = inner.find('|');
  const std::string_view key = inner.substr(0, bar);
  const auto index = find_key(entries_, key);
  if (!index) return std::nullopt;
  const Parsed& info = parsed(*index, Lang::En);
  std::vector<Arg> args;
  std::string_view rest = bar == std::string_view::npos ? std::string_view{} : inner.substr(bar + 1);
  while (!rest.empty()) {
    const std::size_t next = rest.find('|');
    const std::string_view pair = rest.substr(0, next);
    rest = next == std::string_view::npos ? std::string_view{} : rest.substr(next + 1);
    const std::size_t eq = pair.find('=');
    if (eq == std::string_view::npos) continue;
    const std::string_view name = pair.substr(0, eq), value = pair.substr(eq + 1);
    const bool number = std::any_of(info.args.begin(), info.args.end(),
                                    [&](const ArgInfo& a) { return a.name == name && a.type == ArgType::Number; });
    args.push_back(Arg{name, arg_value(value, number)});
  }
  return render(*index, args);
}

int Translator::translate(std::string& out, std::string_view input) {
  int replaced = 0;
  std::string result;
  std::size_t pos = 0;
  for (;;) {
    const std::size_t open = input.find("[[", pos);
    if (open == std::string_view::npos) break;
    const std::size_t close = input.find("]]", open + 2);
    if (close == std::string_view::npos) break;
    result.append(input.substr(pos, open - pos));
    const std::string_view inner = input.substr(open + 2, close - open - 2);
    if (auto expanded = expand(inner)) {
      result += *expanded;
      ++replaced;
    } else {
      // an unknown key stays as written, so a preview shows it
      result.append(input.substr(open, close + 2 - open));
    }
    pos = close + 2;
  }
  if (replaced == 0) {
    out.assign(input);
    return 0;
  }
  result.append(input.substr(pos));
  out = std::move(result);
  return replaced;
}

}  // namespace cadence::i18n

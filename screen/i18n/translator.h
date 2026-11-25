#pragma once

#include "i18n/catalog.h"
#include "i18n/lang.h"
#include "i18n/message.h"
#include "i18n/value.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::i18n {

// the screen's messages in its language (the device's language setting, pushed through config sync). one per app,
// used on the ui thread alone: it parses each message on first use and keeps it.
//
// rmlui: the ui's SystemInterface::TranslateString(out, in) returns translate(out, in), which rewrites every
// [[key]] or [[key|name=value|name=value]] in a text node or attribute; a value is a number when the key's
// argument is, and holds no | or ]]. rmlui translates a document's text when it is loaded, so on_change's
// listener reloads the open documents (live switching).
class Translator {
 public:
  explicit Translator(Lang lang = Lang::En, std::span<const CatalogEntry> entries = builtin_entries());

  [[nodiscard]] Lang language() const { return lang_; }
  // bumps generation() and calls each listener when the language changes
  void set_language(Lang lang);
  // 0, then one more per change: a document stamped with an older one is stale
  [[nodiscard]] std::uint64_t generation() const { return generation_; }
  void on_change(std::function<void(Lang)> listener);

  // a key's message with its arguments: generated key types (keys.h) check both at compile time
  template <class Key>
    requires requires(const Key& k) {
      Key::kIndex;
      k.args();
    }
  [[nodiscard]] std::string tr(const Key& key) {
    const auto args = key.args();
    return render(Key::kIndex, std::span<const Arg>(args.data(), args.size()));
  }
  // by name, for keys only known at run time; an unknown key renders as itself
  [[nodiscard]] std::string tr(std::string_view key, std::span<const Arg> args = {});

  // TranslateString's semantics: out is input with each [[...]] replaced; returns how many were
  int translate(std::string& out, std::string_view input);

 private:
  struct Parsed {
    std::unique_ptr<Message> message;
    std::vector<ArgInfo> args;
  };

  std::string render(std::size_t index, std::span<const Arg> args);
  const Parsed& parsed(std::size_t index, Lang lang);
  // nullopt for a key the catalog lacks
  std::optional<std::string> expand(std::string_view inner);

  Lang lang_;
  std::span<const CatalogEntry> entries_;
  std::uint64_t generation_ = 0;
  std::vector<std::function<void(Lang)>> listeners_;
  // per language (en, ru, xx), per entry
  std::vector<Parsed> cache_[3];
};

}  // namespace cadence::i18n

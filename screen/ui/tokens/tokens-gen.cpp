// tokens-gen: afterglow's tokens.json into the ui's rcss and c++ (design r2 section 25: rmlui has no var(), so the
// tokens are expanded at build time). run by the build, never shipped:
//
//   tokens-gen <tokens.json> <tokens.h> <out dir> <template.rcss>...
//
// writes <out dir>/tokens.rcss (a utility class per colour, tone and type token) and each template with its
// placeholders expanded: $group.name is the token's value ("$color.ember" -> #FF9A3CFF, "$space.s4" -> 16dp,
// "$motion.focus" -> 0.18s cubic-out, "$font.text" -> Inter), and "$type.<name>;" the type token's declarations. a
// template that writes a colour of its own (#rgb...) or names an unknown token fails the build
#include <nlohmann/json.hpp>

#include <algorithm>
#include <cctype>
#include <cstdio>
#include <fstream>
#include <iostream>
#include <map>
#include <regex>
#include <sstream>
#include <string>
#include <vector>

namespace {

using nlohmann::json;

std::string readFile(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  if (!in) throw std::runtime_error("cannot read " + path);
  std::ostringstream s;
  s << in.rdbuf();
  return s.str();
}

void writeFile(const std::string& path, const std::string& data) {
  // unchanged files keep their time, so ninja rebuilds nothing that depends on them
  std::ifstream old(path, std::ios::binary);
  if (old) {
    std::ostringstream s;
    s << old.rdbuf();
    if (s.str() == data) return;
  }
  std::ofstream out(path, std::ios::binary | std::ios::trunc);
  if (!out) throw std::runtime_error("cannot write " + path);
  out << data;
}

std::string dp(double v) {
  std::ostringstream s;
  s << v << "dp";
  return s.str();
}

// #RRGGBB or #RRGGBBAA as #RRGGBBAA
std::string color(const std::string& hex) {
  static const std::regex re("#[0-9A-Fa-f]{6}([0-9A-Fa-f]{2})?");
  if (!std::regex_match(hex, re)) throw std::runtime_error("bad colour " + hex);
  return hex.size() == 7 ? hex + "FF" : hex;
}

std::string seconds(int ms) {
  std::ostringstream s;
  s << static_cast<double>(ms) / 1000.0 << "s";
  return s.str();
}

std::string camel(const std::string& name) {
  std::string out;
  bool up = true;
  for (char c : name) {
    if (c == '-' || c == '_') {
      up = true;
      continue;
    }
    out += up ? static_cast<char>(std::toupper(static_cast<unsigned char>(c))) : c;
    up = false;
  }
  return out;
}

struct Tokens {
  std::map<std::string, std::string> values;
  std::map<std::string, std::string> types;
};

Tokens load(const json& t) {
  Tokens out;
  for (const char* g : {"color", "tone"}) {
    for (auto& [k, v] : t.at(g).items()) out.values[std::string(g) + "." + k] = color(v.get<std::string>());
  }
  for (auto& [k, v] : t.at("scrim").items()) {
    const auto s = v.get<std::string>();
    out.values["scrim." + k] = s.starts_with("#") ? color(s) : s;
  }
  for (auto& [k, v] : t.at("font").items()) out.values["font." + k] = v.get<std::string>();
  for (const char* g : {"space", "radius", "size"}) {
    for (auto& [k, v] : t.at(g).items()) out.values[std::string(g) + "." + k] = dp(v.get<double>());
  }
  for (auto& [k, v] : t.at("motion").items()) {
    out.values["motion." + k] = seconds(v.at("ms").get<int>()) + " " + v.at("ease").get<std::string>();
    out.values["duration." + k] = seconds(v.at("ms").get<int>());
  }
  for (auto& [k, v] : t.at("focus").items()) {
    std::ostringstream s;
    s << v.get<double>();
    out.values["focus." + k] = s.str();
  }
  for (auto& [k, v] : t.at("type").items()) {
    const auto family = t.at("font").at(v.at("family").get<std::string>()).get<std::string>();
    std::ostringstream s;
    s << "font-family: " << family << "; font-weight: " << v.at("weight").get<int>()
      << "; font-size: " << dp(v.at("size").get<double>()) << "; line-height: " << dp(v.at("line").get<double>())
      << "; letter-spacing: " << dp(v.at("tracking").get<double>()) << ";";
    out.types[k] = s.str();
  }
  return out;
}

std::string expand(const Tokens& t, const std::string& src, const std::string& name) {
  // comments are kept but never expanded or checked
  std::string out;
  std::size_t i = 0;
  int line = 1;
  const auto fail = [&](const std::string& why) {
    throw std::runtime_error(name + ":" + std::to_string(line) + ": " + why);
  };
  while (i < src.size()) {
    if (src.compare(i, 2, "/*") == 0) {
      const auto end = src.find("*/", i + 2);
      if (end == std::string::npos) fail("unterminated comment");
      for (std::size_t j = i; j < end; ++j) line += src[j] == '\n';
      out.append(src, i, end + 2 - i);
      i = end + 2;
      continue;
    }
    const char c = src[i];
    if (c == '\n') ++line;
    if (c == '#' && i + 1 < src.size() && std::isxdigit(static_cast<unsigned char>(src[i + 1]))) {
      // an id selector (#name) starts with a letter that is not a hex digit, or has non-hex letters after it
      std::size_t j = i + 1;
      while (j < src.size() && (std::isalnum(static_cast<unsigned char>(src[j])) || src[j] == '-' || src[j] == '_')) ++j;
      const std::string word = src.substr(i + 1, j - i - 1);
      if (std::all_of(word.begin(), word.end(), [](char ch) { return std::isxdigit(static_cast<unsigned char>(ch)); }) &&
          (word.size() == 3 || word.size() == 4 || word.size() == 6 || word.size() == 8)) {
        fail("a literal colour #" + word + ": use a token");
      }
    }
    if (c == '$') {
      std::size_t j = i + 1;
      while (j < src.size() && (std::isalnum(static_cast<unsigned char>(src[j])) || src[j] == '-' || src[j] == '.' ||
                                src[j] == '_')) {
        ++j;
      }
      std::string key = src.substr(i + 1, j - i - 1);
      while (!key.empty() && key.back() == '.') {
        key.pop_back();
        --j;
      }
      if (key.starts_with("type.")) {
        const auto it = t.types.find(key.substr(5));
        if (it == t.types.end()) fail("unknown type token " + key);
        out += it->second;
        // "$type.x;" leaves no empty declaration
        if (j < src.size() && src[j] == ';') ++j;
      } else {
        const auto it = t.values.find(key);
        if (it == t.values.end()) fail("unknown token $" + key);
        out += it->second;
      }
      i = j;
      continue;
    }
    out += c;
    ++i;
  }
  return out;
}

std::string utilities(const json& tj, const Tokens& t) {
  std::ostringstream s;
  s << "/* generated by tokens-gen from tokens.json; do not edit */\n";
  for (auto& [k, v] : tj.at("color").items()) {
    (void)v;
    s << ".fg-" << k << " { color: " << t.values.at("color." + k) << "; }\n";
    s << ".bg-" << k << " { background-color: " << t.values.at("color." + k) << "; }\n";
  }
  for (auto& [k, v] : tj.at("tone").items()) {
    (void)v;
    s << ".tone-" << k << " { color: " << t.values.at("tone." + k) << "; image-color: " << t.values.at("tone." + k)
      << "; }\n";
    s << ".stripe-" << k << " { background-color: " << t.values.at("tone." + k) << "; }\n";
  }
  for (const auto& [k, v] : t.types) s << ".t-" << k << " { " << v << " }\n";
  for (auto& [k, v] : tj.at("space").items()) {
    s << ".gap-" << k << " { gap: " << dp(v.get<double>()) << "; }\n";
  }
  return s.str();
}

std::string header(const json& t) {
  std::ostringstream s;
  s << "// generated by tokens-gen from screen/ui/tokens/tokens.json; do not edit\n#pragma once\n\n"
       "#include <array>\n#include <cstdint>\n#include <string_view>\n\nnamespace cadence::screen::ui::tokens {\n\n";
  for (auto& [k, v] : t.at("motion").items()) {
    s << "inline constexpr int kMotion" << camel(k) << "Ms = " << v.at("ms").get<int>() << ";\n";
  }
  for (auto& [k, v] : t.at("focus").items()) {
    s << "inline constexpr double kFocus" << camel(k) << " = " << v.get<double>() << ";\n";
  }
  for (auto& [k, v] : t.at("timing").items()) {
    s << "inline constexpr int k" << camel(k) << " = " << v.get<int>() << ";\n";
  }
  for (const char* g : {"space", "size", "radius"}) {
    for (auto& [k, v] : t.at(g).items()) {
      s << "inline constexpr double k" << camel(g) << camel(k) << " = " << v.get<double>() << ";\n";
    }
  }
  s << "\nstruct Icon {\n  std::string_view name;\n  char32_t codepoint;\n};\n\n";
  std::vector<std::string> icons;
  for (auto& [k, v] : t.at("icons").items()) {
    (void)v;
    if (!k.starts_with("_")) icons.push_back(k);
  }
  // nlohmann's object keeps keys sorted; the font build numbers them in the file's order, so read that order
  s << "// the icon font's codepoints (screen/ui/fonts/CadenceIcons.ttf), U+E000 onwards in tokens.json's order\n";
  s << "inline constexpr std::array<Icon, " << icons.size() << "> kIcons{{\n";
  return s.str();
}

}  // namespace

int main(int argc, char** argv) {
  if (argc < 4) {
    std::cerr << "usage: tokens-gen <tokens.json> <tokens.h> <out dir> <template.rcss>...\n";
    return 2;
  }
  try {
    const std::string raw = readFile(argv[1]);
    const json tj = json::parse(raw);
    // ordered_json keeps the file's order, the order the font build numbers the icons in
    const auto ordered = nlohmann::ordered_json::parse(raw);
    const Tokens t = load(tj);

    std::string h = header(tj);
    std::uint32_t code = 0xE000;
    std::size_t n = 0;
    for (auto& [k, v] : ordered.at("icons").items()) {
      (void)v;
      if (k.starts_with("_")) continue;
      char buf[64];
      std::snprintf(buf, sizeof buf, "0x%04X", code++);
      h += "    {\"" + k + "\", " + buf + "},\n";
      ++n;
    }
    h += "}};\n\n}  // namespace cadence::screen::ui::tokens\n";
    (void)n;
    writeFile(argv[2], h);

    const std::string dir = argv[3];
    writeFile(dir + "/tokens.rcss", utilities(tj, t));
    for (int i = 4; i < argc; ++i) {
      const std::string path = argv[i];
      std::string base = path.substr(path.find_last_of('/') + 1);
      writeFile(dir + "/" + base, "/* generated by tokens-gen from " + base + "; do not edit */\n" +
                                      expand(t, readFile(path), base));
    }
  } catch (const std::exception& e) {
    std::cerr << "tokens-gen: " << e.what() << "\n";
    return 1;
  }
  return 0;
}

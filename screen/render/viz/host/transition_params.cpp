#include "transition.h"

#include <cstdlib>
#include <regex>
#include <sstream>

namespace cadence::render::viz {

Transition crossfade() {
  return {"crossfade", "vec4 transition(vec2 uv) { return mix(getFromColor(uv), getToColor(uv), progress); }\n"};
}

std::vector<TransitionParam> transition_params(const std::string& glsl) {
  static const std::regex decl(R"(^\s*uniform\s+(float|int|bool|vec2|vec3|vec4|ivec2)\s+(\w+)\s*;\s*//\s*=\s*([^;\n]+);?)");
  std::vector<TransitionParam> out;
  std::istringstream in(glsl);
  std::string line;
  while (std::getline(in, line)) {
    std::smatch m;
    if (!std::regex_search(line, m, decl)) continue;
    TransitionParam p{m[1], m[2], {}};
    std::string v = m[3];
    // vec2(1.0, -1.0): the numbers inside the constructor, not its name's digit
    if (const auto open = v.find('('); open != std::string::npos) v = v.substr(open + 1);
    if (v.find("true") != std::string::npos) p.values = {1};
    else if (v.find("false") != std::string::npos) p.values = {0};
    else {
      static const std::regex num(R"([-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?)");
      for (auto it = std::sregex_iterator(v.begin(), v.end(), num); it != std::sregex_iterator(); ++it) {
        p.values.push_back(std::strtof(it->str().c_str(), nullptr));
      }
    }
    if (!p.values.empty()) out.push_back(std::move(p));
  }
  return out;
}

}  // namespace cadence::render::viz

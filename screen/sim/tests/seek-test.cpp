// the sim's seek check: plays, then three quick → with the osd hidden, while the placeholder player (as a slow mpv)
// keeps reporting the position it read before each seek until the seek lands 600 ms later. the bar (the screen
// context's position) must jump to the added-up target at once and never go back to a reading from before it. a
// plain main, no catch2
#include <unistd.h>

#include <cstdio>
#include <cstdlib>
#include <string>
#include <vector>

namespace {

int fail(const std::string& what) {
  std::fprintf(stderr, "sim-seek: fail: %s\n", what.c_str());
  return 1;
}

}  // namespace

int main() {
  const std::string dir = "/tmp/cadence-screen-sim-seek." + std::to_string(::getpid());
  const std::string scriptPath = dir + "/seek.txt";
  std::string mkdir = "mkdir -p " + dir;
  if (std::system(mkdir.c_str()) != 0) return fail("mkdir");
  std::string script = "start --headless\n"
                       "wait-for route:home 15000\n"
                       "send {\"route\": {\"route\": \"title\", \"ref\": \"ref-tg-1\"}}\n"
                       "wait-for route:title 15000\n"
                       "wait 2000\n"
                       "press OK\n"
                       "wait-for route:picker 10000\n"
                       "press OK\n"
                       "wait-for route:player 20000\n"
                       "wait-for media:playing 10000\n"
                       // the osd shows for 3 s at the start: hidden, ← → seek
                       "wait 3600\n"
                       "context\n"
                       "press RIGHT\n"
                       "press RIGHT\n"
                       "press RIGHT\n";
  // the bar every 50 ms over the seeks' landing and after
  for (int i = 0; i < 30; ++i) script += "context\nwait 50\n";
  script += "quit\n";
  FILE* out = std::fopen(scriptPath.c_str(), "w");
  if (out == nullptr) return fail("cannot write " + scriptPath);
  std::fputs(script.c_str(), out);
  std::fclose(out);

  const std::string command = std::string(CADENCE_SIM_BIN) + " --headless run " + scriptPath + " 2>/dev/null";
  FILE* run = ::popen(command.c_str(), "r");
  if (run == nullptr) return fail("popen");
  std::vector<unsigned long long> positions;
  char line[4096];
  while (std::fgets(line, sizeof line, run) != nullptr) {
    const std::string l = line;
    if (!l.starts_with("context ")) continue;
    const auto at = l.find("position_ms=");
    if (at == std::string::npos) continue;
    positions.push_back(std::strtoull(l.c_str() + at + 12, nullptr, 10));
  }
  const int code = ::pclose(run);
  if (code != 0) return fail("the run exited " + std::to_string(code));
  if (positions.size() < 20) return fail("only " + std::to_string(positions.size()) + " positions");
  const auto before = positions.front();
  // three steps of 10 s, added up from the held target (not from a reading of before a seek)
  const auto target = before + 30000;
  if (positions[1] + 500 < target || positions[1] > target + 1500) {
    return fail("after three presses the bar is at " + std::to_string(positions[1]) + ", not ~" + std::to_string(target));
  }
  for (std::size_t i = 2; i < positions.size(); ++i) {
    if (positions[i] + 200 < positions[i - 1]) {
      return fail("the bar went back from " + std::to_string(positions[i - 1]) + " to " + std::to_string(positions[i]));
    }
    if (positions[i] + 500 < target) {
      return fail("a reading from before the seek moved the bar to " + std::to_string(positions[i]));
    }
  }
  std::printf("sim-seek: ok (%llu -> %llu, %zu readings)\n", before, positions.back(), positions.size());
  return 0;
}

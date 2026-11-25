// the server's resolving step for a link it plays (media.video/prepare), run in the sim headless as a child process:
// the step opens at once over home, its failure offers «Назад» alone, which goes home. a plain main, no catch2
#include <unistd.h>

#include <cstdio>
#include <cstdlib>
#include <string>

namespace {

int fail(const std::string& what) {
  std::fprintf(stderr, "sim-prepare: fail: %s\n", what.c_str());
  return 1;
}

}  // namespace

int main() {
  const std::string dir = "/tmp/cadence-screen-sim-prepare." + std::to_string(::getpid());
  const std::string scriptPath = dir + "/prepare.txt";
  if (std::system(("mkdir -p " + dir).c_str()) != 0) return fail("mkdir");
  // wait-for fails the run (a non-zero exit) when its route never comes
  const std::string script = "start --headless\n"
                             "wait-for route:home 15000\n"
                             "wait 4000\n"
                             "send {\"media_preparing\": {\"state\": \"resolving\"}}\n"
                             "wait-for route:resolving 5000\n"
                             "send {\"media_preparing\": {\"state\": \"resolving\", \"title\": \"T\", \"subtitle\": \"C\"}}\n"
                             "wait 300\n"
                             "wait-for route:resolving 1000\n"
                             "send {\"media_preparing\": {\"state\": \"failed\", \"code\": \"unplayable\"}}\n"
                             "wait 300\n"
                             "press OK\n"
                             "wait-for route:home 5000\n"
                             "quit\n";
  FILE* out = std::fopen(scriptPath.c_str(), "w");
  if (out == nullptr) return fail("cannot write " + scriptPath);
  std::fputs(script.c_str(), out);
  std::fclose(out);
  const int code = std::system((std::string(CADENCE_SIM_BIN) + " --headless run " + scriptPath).c_str());
  if (code != 0) return fail("the run exited " + std::to_string(code));
  std::printf("sim-prepare: ok\n");
  return 0;
}

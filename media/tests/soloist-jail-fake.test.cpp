#include "audiod/soloist-supervisor.h"
#include "support/eventually.h"
#include "support/jail-rig.h"
#include "support/soloist-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <link.h>

#include <chrono>
#include <filesystem>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;
using State = SoloistState;

namespace {

// the shared objects this process loaded from the system: a superset of what the fake needs (the loader, libc,
// libm, the c++ runtime)
std::vector<std::string> systemLibraries() {
  std::vector<std::string> found;
  dl_iterate_phdr(
      [](dl_phdr_info* info, std::size_t, void* data) {
        const std::string name = info->dlpi_name == nullptr ? "" : info->dlpi_name;
        if (name.starts_with("/lib") || name.starts_with("/usr/lib"))
          static_cast<std::vector<std::string>*>(data)->push_back(name);
        return 0;
      },
      &found);
  return found;
}

// what the trixie runtime holds, built from the builder's own loader and libraries and our shim: regular files at the
// paths the loader looks in, as the runtime assembly lays them out (no symlinks)
void fillRuntime(const std::string& root) {
  std::string libraryDir;
  for (const auto& library : systemLibraries()) {
    const auto target = std::filesystem::path(root + library);
    std::filesystem::create_directories(target.parent_path());
    std::filesystem::copy_file(std::filesystem::canonical(library), target,
                               std::filesystem::copy_options::overwrite_existing);
    std::filesystem::permissions(target, std::filesystem::perms(0755));
    if (target.filename().string().starts_with("libc.so"))
      libraryDir = target.parent_path().string();
  }
  REQUIRE_FALSE(libraryDir.empty());
  std::filesystem::copy_file(CADENCE_PULSE_LIBRARY, libraryDir + "/libpulse.so.0",
                             std::filesystem::copy_options::overwrite_existing);
  std::filesystem::permissions(libraryDir + "/libpulse.so.0", std::filesystem::perms(0755));
  for (auto dir = std::filesystem::path(libraryDir); dir.string().size() > root.size(); dir = dir.parent_path())
    std::filesystem::permissions(dir, std::filesystem::perms(0755));
}

}  // namespace

TEST_CASE("the fake soloist runs through the real jail and plays through the shim in it") {
  requireJail();
  SoloistRig rig;
  fillRuntime(rig.jail().root());
  rig.run();

  const auto status = rig.reports().waitState(State::Ready, 20s);
  REQUIRE(status);
  // its version was probed through the jail too
  CHECK(status->version == "1.3.10.18");
  REQUIRE(rig.fake().started());
  const std::vector<std::string> expected{"--device-name",
                                          "Kitchen",
                                          "--api-key",
                                          std::string(kFakeKey),
                                          "--data-dir",
                                          "/home/soloist",
                                          "--cache-dir",
                                          "/home/soloist/cache",
                                          "--cache-size",
                                          "100",
                                          "--initial-volume",
                                          "35",
                                          "--ws",
                                          "127.0.0.1:0",
                                          "--env--",
                                          "HOME=/home/soloist",
                                          "SPOTIFY_SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
                                          "PATH=/"};
  CHECK(rig.fake().argv() == expected);

  // the shim in the jail reaches the pcm socket at /run/cadence/pcm.sock, which takes its audio even unheard
  rig.fake().send("!tone on");
  CHECK(eventually([&] { return rig.fake().tone() > 44100 * 4 / 2; }, 10s));

  rig.supervisor().probe("jailed", rig.build());
  const auto probe = rig.reports().probe("jailed");
  REQUIRE(probe);
  CHECK(probe->code.empty());
}

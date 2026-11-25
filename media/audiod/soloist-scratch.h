#pragma once

#include "audiod/log.h"

#include <optional>
#include <string>

namespace cadence::audiod {

// soloist's scratch (reference §1.1): its data dir and the run dir its pcm socket lives in, bound into the jail
struct SoloistScratch {
  std::string root;
  // root:nobody 1770, sticky: soloist creates its files here but cannot remove or rename the crashpad dir in it
  std::string home;
  // nobody 0700
  std::string run;
};

// recreates the scratch at root, once per audiod run: whatever sits there is removed without following a symlink at
// any depth, then root (root 0755), home, run and home/crashpad (root 0555, a directory: soloist makes the path with
// create_directories, which throws on a file, and with a read-only one its crash dumps have nowhere to go) are made
// through descriptors, each private until its owner and mode are final. owners change only as root. root's parent must
// exist. null, logged (soloist_scratch_failed call=… errno=…), when it cannot
std::optional<SoloistScratch> prepareSoloistScratch(const Log& log, const std::string& root);

// home alone made again, as prepareSoloistScratch makes it, while run and the pcm socket in it stay: soloist owns what
// it writes there, and a ws.port it made a directory cannot be cleared otherwise. false, logged, when it cannot
bool resetSoloistHome(const Log& log, const SoloistScratch& scratch);

}  // namespace cadence::audiod

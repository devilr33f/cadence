#pragma once

// the screen's way to the milkdrop presets a device ships: the directory resolved per platform and one source a
// preset. no gl here: call it from any thread, then hand the sources to the Renderer (Host) before its init, which
// runs on the viz thread with its context current. without projectM (CADENCE_VIZ_PROJECTM unset) both are empty
#include "../host/source.h"
#include "milkdrop_source.h"

#include <memory>
#include <string>
#include <vector>

namespace cadence::render::viz {

// true when this build links libprojectM (the android-tv apk, the rpi deb, the macos simulator built with it)
bool milkdrop_built();

// the shipped preset directory, or empty when there is none:
//   $CADENCE_SCREEN_MILKDROP when set (rpi's unit: /opt/cadence/share/viz/milkdrop; android-main.cpp: the app's
//   files/viz/milkdrop, which UiAssets unpacked from the apk's assets/viz/milkdrop),
//   else viz/milkdrop beside the first root of $CADENCE_SCREEN_UI_ASSETS (<share>/ui -> <share>/viz/milkdrop),
//   else the build's own (CADENCE_VIZ_MILKDROP_DIR, the simulator's fetched copy).
// a directory that does not exist counts as none
std::string milkdrop_dir();

// one MilkdropSource per preset of dir: the names its presets.txt lists, in order (the shipped, curated order), else
// every .milk under it, sorted; a name with no file is left out. ids are "milkdrop:<name>"
std::vector<std::unique_ptr<VizSource>> milkdrop_sources(const std::string& dir, MilkdropConfig c = {});

}  // namespace cadence::render::viz

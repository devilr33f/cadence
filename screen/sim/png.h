#pragma once

#include "render/capture.h"

#include <string>

namespace cadence::screen::sim {

// screenshots (and the smoke test's check) through stb, as the gallery's preview does
bool writePng(const std::string& path, const render::Image& image);
bool readPng(const std::string& path, render::Image& image);

}  // namespace cadence::screen::sim

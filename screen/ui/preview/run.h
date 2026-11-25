#pragma once

#include "ui/preview/gallery.h"

#include <string>
#include <vector>

namespace cadence::screen::ui::preview {

// a gallery's command line over its pages, golden images in golden (see main.cpp): the exit code
int runGallery(int argc, char** argv, const std::vector<Page>& pages, const std::string& golden, const char* name);

}  // namespace cadence::screen::ui::preview

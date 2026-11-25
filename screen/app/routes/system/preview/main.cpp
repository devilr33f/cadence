// cadence-system-preview: i3's gallery (the drawer's states, tasks, enrollment, airplay, settings) with
// cadence-ui-preview's options and checks, golden images in screen/app/routes/system/golden
#include "app/routes/system/preview/pages.h"
#include "ui/preview/run.h"

int main(int argc, char** argv) {
  using namespace cadence::screen;
  return ui::preview::runGallery(argc, argv, routes::syspreview::pages(), CADENCE_SCREEN_SYSTEM_GOLDEN,
                                 "cadence-system-preview");
}

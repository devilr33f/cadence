// cadence-playback-preview: i2's routes offscreen with a fake player and server (see cadence-ui-preview for the
// options)
#include "app/routes/preview/playback-pages.h"
#include "ui/preview/run.h"

int main(int argc, char** argv) {
  return cadence::screen::ui::preview::runGallery(argc, argv, cadence::screen::routes::playback::pages(),
                                                  CADENCE_SCREEN_PLAYBACK_GOLDEN, "cadence-playback-preview");
}

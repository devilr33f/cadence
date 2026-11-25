// cadence-browse-preview: i1's routes offscreen, on the fake catalog (see cadence-ui-preview for the options)
#include "app/routes/preview/pages.h"
#include "ui/preview/run.h"

int main(int argc, char** argv) {
  return cadence::screen::ui::preview::runGallery(argc, argv, cadence::screen::routes::preview::pages(),
                                                  CADENCE_SCREEN_BROWSE_GOLDEN, "cadence-browse-preview");
}

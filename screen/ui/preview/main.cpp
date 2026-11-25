// cadence-ui-preview: the gallery offscreen.
//
//   cadence-ui-preview [--out DIR] [--page NAME] [--lang ru|en|xx] [--size WxH] [--check] [--update-golden]
//
// renders every page (or one) to DIR/<page>.png and writes DIR/report.txt (design r2 section 25: frame time, the
// full-screen translucent layers, flagged over 2, and the animated area, flagged over 30 %). --check also compares
// each page with its golden image (preview/golden/<page>.png, ru, 960 x 540), renders it twice for a stable frame,
// walks every focusable node with the d-pad, and loads every page in the pseudo-locale xx to find overflowing text;
// it exits 1 on any failure or flag. --update-golden rewrites the golden images
#include "ui/preview/run.h"

int main(int argc, char** argv) {
  using namespace cadence::screen::ui::preview;
  return runGallery(argc, argv, pages(), CADENCE_SCREEN_UI_GOLDEN, "cadence-ui-preview");
}

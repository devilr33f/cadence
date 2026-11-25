#pragma once

#include "ui/rml/sprites.h"

namespace cadence::screen::ui::images {

// a soft, near-gaussian blur (three box passes each way, edges clamped) of premultiplied rgba, in place. a backdrop
// is a poster scaled up several times (no wider artwork comes from the catalog): blurred at its decoded size first,
// the gpu's linear filter scales up a picture with no edges left to step or block. radius in pixels of the image;
// 0 or an empty image changes nothing. runs on a decode worker
void blur(Pixels& image, int radius);

// the image cropped to width x height's shape about its centre and resampled (bilinear) to exactly that size, up or
// down: a backdrop is blurred at its slot's (capped) size, so the gpu scales up a smooth picture, never the steps of
// a poster's few pixels. false when either is empty
bool fitCover(Pixels& image, int width, int height);

// clears each corner outside a circle of radius pixels, antialiased over a pixel, of premultiplied rgba in place: an
// image drawn rounded by its decorator alone, so its box needs no clip mask (a stencil each frame). 0 changes nothing
void roundCorners(Pixels& image, int radius);

}  // namespace cadence::screen::ui::images

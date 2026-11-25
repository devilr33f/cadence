#pragma once

#include "app/routes/preview/playback-fakes.h"
#include "ui/preview/gallery.h"

#include <string>
#include <vector>

// i2's gallery: every playback page and state with fake data (cadence-playback-preview, ctest playback-gallery), for
// the golden images, the d-pad walk, the pseudo-locale and the budget report. no network, no device, no sound
namespace cadence::screen::routes::playback {

std::vector<ui::preview::Page> pages();

// fake data: an anime episode (torrplayer, chapters, skips, three track kinds, up next), a youtube video (item
// actions, panels), a music track; the player's state at a position; the catalog's and the queue's answers
Session fakeEpisode();
Session fakeVideo();
Session fakeTrack();
player::PlayerState fakeState(const Session& s, std::uint64_t positionMs, cadence_v2_PlaybackState state);
std::string fakeDetailJson();
std::string fakeOptionsJson(bool nextMissing = false);
QueuePage fakeQueue();

}  // namespace cadence::screen::routes::playback

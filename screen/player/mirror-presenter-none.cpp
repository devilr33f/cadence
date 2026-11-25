// no mirror decoder on this platform yet (linux: the pi's v4l2m2m path into the texture is a follow-up), so the
// screen's Hello says it does not mirror and cadenced keeps mirroring off for it
#include "player/mirror-presenter.h"

namespace cadence::screen::player {

std::unique_ptr<MirrorPresenter> MirrorPresenter::create() { return nullptr; }

}  // namespace cadence::screen::player

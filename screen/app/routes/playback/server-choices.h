#pragma once

#include "app/routes/playback/context.h"

#include <string>

// the choices of a video the server plays from a link (a dashboard's YouTube link): its audio languages and a music
// video's views, from media.video/play, each switched with rpc media.video {sessionId, quality|audio|view} from where
// it plays (the server sends the new play). the player's osd and audio mode show them as chips with a sheet each
namespace cadence::screen::routes::choices {

// a choice's words: "Русский · дубляж", "Видео"
std::string audioLabel(const AudioChoice& a);
std::string viewLabel(View v);
// the chip's words for what plays now; "" when the session has no such choice
std::string currentAudio(const Session& s);
std::string currentView(const Session& s);
// the sheets; their chips' actions are "schoice:audio:<id>" and "schoice:view:<name>"
void openAudio(PlaybackContext& c);
void openViews(PlaybackContext& c);
// a sheet's chip, or a quality picked on a server video: rpc media.video; true when the action was one of these
bool activate(PlaybackContext& c, const std::string& action);
// a quality of a server video (the osd's quality sheet)
void switchQuality(PlaybackContext& c, const std::string& name);

}  // namespace cadence::screen::routes::choices

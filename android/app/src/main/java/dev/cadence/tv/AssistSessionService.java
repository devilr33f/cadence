package dev.cadence.tv;

import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.service.voice.VoiceInteractionSession;
import android.service.voice.VoiceInteractionSessionService;
import android.view.Window;
import android.view.WindowManager;

// one press of the remote's assistant button (G): a session the system shows within ~10 ms (S4b). it shows nothing
// itself: it hands the press to the screen, which opens the assistant (cadenced starts the turn through the room's
// speaker: the remote's microphone delivers nothing to an app, S4b), and hides at once, so it never leaves an overlay
// over the activity
public final class AssistSessionService extends VoiceInteractionSessionService {
    @Override
    public VoiceInteractionSession onNewSession(Bundle args) {
        return new Session(this);
    }

    static final class Session extends VoiceInteractionSession {
        Session(Context context) {
            super(context);
        }

        // the system adds the session's window fullscreen before onShow runs; an empty one must neither dim nor
        // take a key from the activity under it while it is up (it hides in onShow, a frame or two later). the
        // session's dialog and its window exist from onCreate, not in the constructor
        @Override
        public void onCreate() {
            super.onCreate();
            Window w = getWindow() != null ? getWindow().getWindow() : null;
            if (w == null)
                return;
            w.clearFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND);
            w.addFlags(WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE | WindowManager.LayoutParams.FLAG_NOT_TOUCHABLE);
        }

        @Override
        public void onShow(Bundle args, int showFlags) {
            super.onShow(args, showFlags);
            try {
                Intent i = new Intent(getContext(), CadenceActivity.class)
                    .setAction(CadenceActivity.ACTION_ASSIST)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP);
                // a plain start, so the screen's one instance gets it (an app whose voice interaction service the
                // system binds may start activities from the background)
                getContext().startActivity(i);
            } finally {
                hide();
            }
        }
    }
}

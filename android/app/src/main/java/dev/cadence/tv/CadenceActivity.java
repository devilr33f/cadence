package dev.cadence.tv;

import android.content.Context;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.PixelFormat;
import android.media.AudioManager;
import android.os.Bundle;
import android.view.KeyEvent;
import android.view.SurfaceHolder;
import android.view.SurfaceView;
import android.view.ViewGroup;
import android.view.WindowManager;
import android.widget.RelativeLayout;

import org.libsdl.app.SDLActivity;
import org.libsdl.app.SDLSurface;

// cadence-screen (libmain.so) on SDL3. S1's layout: mpv decodes to its own SurfaceView (vo=mediacodec_embed) at the
// bottom of the layout, and SDL's surface sits above it, translucent, with the ui alone drawn on a transparent clear.
// the video surface is handed to the screen's SurfaceEmbedOutput as it changes, and taken back before
// surfaceDestroyed returns. the remote's assistant button arrives as an ASSIST intent from AssistSession and is given
// to the screen as its assistant key. the same video surface is MediaCodec's for airplay mirroring (the screen's
// mirror presenter) while no film plays. it starts the daemon service, which outlives it
public final class CadenceActivity extends SDLActivity {
    static final String ACTION_ASSIST = "dev.cadence.tv.ASSIST";

    // the screen's surface hand-off (screen/android-main.cpp): a surface and its size, or null as it goes away
    static native void nativeVideoSurface(Object surface, int width, int height);

    // the remote's assistant button, as the screen's assistant key
    static native void nativeAssist();

    // the video plane's rect in the layout's pixels (screen/player/video-fit.h: the content's shape fitted to the
    // overscan's safe area): mpv's mediacodec_embed and the mirror's MediaCodec scale to fit their surface, so the
    // SurfaceView itself takes the shape and the window's black shows around it. kept here, so a recreated activity
    // places its view as the last one did
    private static final Object rectLock = new Object();
    private static int[] placedRect;
    private static CadenceActivity current;

    private SurfaceView videoView;

    // the screen's VideoLayout (android-main.cpp), from any of its threads
    static void videoRect(int x, int y, int w, int h) {
        final CadenceActivity a;
        synchronized (rectLock) {
            placedRect = new int[] {x, y, w, h};
            a = current;
        }
        if (a != null)
            a.runOnUiThread(a::placeVideo);
    }

    private void placeVideo() {
        final int[] r;
        synchronized (rectLock) {
            r = placedRect;
        }
        if (videoView == null || r == null)
            return;
        RelativeLayout.LayoutParams lp = (RelativeLayout.LayoutParams) videoView.getLayoutParams();
        if (lp.leftMargin == r[0] && lp.topMargin == r[1] && lp.width == r[2] && lp.height == r[3])
            return;
        lp.leftMargin = r[0];
        lp.topMargin = r[1];
        lp.width = r[2];
        lp.height = r[3];
        videoView.setLayoutParams(lp);
    }

    @Override
    protected String[] getLibraries() {
        return new String[] {"SDL3", "main"};
    }

    @Override
    protected String[] getArguments() {
        return new String[0];
    }

    @Override
    protected SDLSurface createSDLSurface(Context context) {
        SDLSurface s = super.createSDLSurface(context);
        s.getHolder().setFormat(PixelFormat.TRANSLUCENT);
        s.setZOrderMediaOverlay(true);
        return s;
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        DaemonService.start(this);
        // the ui's rcss and fonts as files before sdl's thread runs the screen
        UiAssets.extract(this);
        super.onCreate(savedInstanceState);
        if (mLayout == null)
            return; // sdl refused to start (a missing library): it shows its own message
        videoView = new SurfaceView(this);
        videoView.getHolder().addCallback(new SurfaceHolder.Callback() {
            @Override
            public void surfaceCreated(SurfaceHolder h) {}

            @Override
            public void surfaceChanged(SurfaceHolder h, int format, int w, int hh) {
                nativeVideoSurface(h.getSurface(), w, hh);
            }

            @Override
            public void surfaceDestroyed(SurfaceHolder h) {
                nativeVideoSurface(null, 0, 0);
            }
        });
        // index 0: below sdl's surface; whole until the screen places it
        mLayout.addView(videoView, 0, new RelativeLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT,
            ViewGroup.LayoutParams.MATCH_PARENT));
        // the bars around a placed plane are black: sdl's surface clears the window over the whole screen, and under
        // an opaque activity nothing else is composed there (the layout's own black covers any moment before that)
        mLayout.setBackgroundColor(Color.BLACK);
        synchronized (rectLock) {
            current = this;
        }
        placeVideo();
        handle(getIntent());
    }

    @Override
    protected void onDestroy() {
        synchronized (rectLock) {
            if (current == this)
                current = null;
        }
        super.onDestroy();
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        handle(intent);
    }

    // the remote's volume keys while cadence is in front: android's own step on STREAM_MUSIC, which is the device's
    // level (SystemVolume), without android's panel; the daemon reads the move back and the screen shows cadence's
    // overlay. the owner's own press (rule 7). a fixed volume (hdmi-cec passthrough) is left to android, which sends
    // it to the tv. sdl leaves the up and down keys to android, and would give mute to the screen, which acts on none
    @Override
    public boolean dispatchKeyEvent(KeyEvent event) {
        final int code = event.getKeyCode();
        if (code == KeyEvent.KEYCODE_VOLUME_UP || code == KeyEvent.KEYCODE_VOLUME_DOWN
            || code == KeyEvent.KEYCODE_VOLUME_MUTE) {
            AudioManager audio = getSystemService(AudioManager.class);
            if (audio != null && !audio.isVolumeFixed()) {
                if (event.getAction() == KeyEvent.ACTION_DOWN) {
                    final int direction = code == KeyEvent.KEYCODE_VOLUME_UP ? AudioManager.ADJUST_RAISE
                        : code == KeyEvent.KEYCODE_VOLUME_DOWN ? AudioManager.ADJUST_LOWER
                        : AudioManager.ADJUST_TOGGLE_MUTE;
                    // a held key repeats; a held mute toggles once
                    if (code != KeyEvent.KEYCODE_VOLUME_MUTE || event.getRepeatCount() == 0)
                        audio.adjustStreamVolume(AudioManager.STREAM_MUSIC, direction, 0);
                }
                return true;
            }
        }
        return super.dispatchKeyEvent(event);
    }

    private void handle(Intent intent) {
        if (intent != null && ACTION_ASSIST.equals(intent.getAction()))
            nativeAssist();
    }
}

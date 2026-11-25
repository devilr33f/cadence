package dev.cadence.tv;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.database.ContentObserver;
import android.media.AudioManager;
import android.os.Handler;
import android.os.HandlerThread;
import android.provider.Settings;
import android.util.Log;

// android's STREAM_MUSIC as the device's one level (device/boards/android-tv: OutputConfig.system_volume). cadenced
// owns the level; audiod's engine in this process moves the stream through write when cadenced sets a level, and reads
// it back through read. a change the system makes itself (the remote's keys android handles while another app is in
// front, another app, the tv's hdmi-cec volume) comes as the volume broadcast or a settings change, and nativeChanged
// tells audiod, which reads the stream again on its own thread and reports a move to cadenced (SystemVolume), so the
// server and the dashboard see the level the tv plays at. the stream's index is offset by its minimum, so audiod sees
// 0..max-min. logs carry codes alone
final class SystemVolume {
    private static final String TAG = "cadence-volume";
    // hidden in the sdk, sent by AudioService on every stream volume and mute change since android 4
    private static final String VOLUME_CHANGED = "android.media.VOLUME_CHANGED_ACTION";
    private static final String MUTE_CHANGED = "android.media.STREAM_MUTE_CHANGED_ACTION";
    private static final String EXTRA_STREAM = "android.media.EXTRA_VOLUME_STREAM_TYPE";

    static {
        // the daemon library holds the natives; the service starts this before AudiodHost first loads it
        System.loadLibrary("cadence-daemon");
    }

    private static volatile AudioManager audio;

    private final Context context;
    private HandlerThread thread;
    private BroadcastReceiver receiver;
    private ContentObserver observer;

    SystemVolume(Context context) {
        this.context = context;
    }

    // caches the class and its methods for audiod's thread (android/native/volume-jni.cpp)
    private static native void nativeInit();

    // the stream may have moved: audiod reads it again (any thread)
    private static native void nativeChanged();

    // before audiod's engine starts: its first Configure reads the stream
    void start() {
        audio = context.getSystemService(AudioManager.class);
        nativeInit();
        thread = new HandlerThread("cadence-volume");
        thread.start();
        Handler handler = new Handler(thread.getLooper());
        receiver = new BroadcastReceiver() {
            @Override
            public void onReceive(Context c, Intent intent) {
                int stream = intent.getIntExtra(EXTRA_STREAM, AudioManager.STREAM_MUSIC);
                if (stream == AudioManager.STREAM_MUSIC)
                    nativeChanged();
            }
        };
        IntentFilter filter = new IntentFilter(VOLUME_CHANGED);
        filter.addAction(MUTE_CHANGED);
        context.registerReceiver(receiver, filter, null, handler);
        // the broadcast is hidden api: the persisted volume settings are the fallback (AudioService writes them about
        // half a second after a change); a notice for a change audiod already read is a no-op there
        observer = new ContentObserver(handler) {
            @Override
            public void onChange(boolean selfChange) {
                nativeChanged();
            }
        };
        context.getContentResolver().registerContentObserver(Settings.System.CONTENT_URI, true, observer);
        Log.i(TAG, "system_volume_started fixed=" + audio.isVolumeFixed());
    }

    void stop() {
        if (receiver != null)
            context.unregisterReceiver(receiver);
        if (observer != null)
            context.getContentResolver().unregisterContentObserver(observer);
        if (thread != null)
            thread.quitSafely();
        receiver = null;
        observer = null;
        thread = null;
    }

    // from audiod's thread: {index, max, muted}, the index and max offset by the stream's minimum, or null when there
    // is no stream volume to move (a fixed volume: hdmi-cec passthrough), and the level stays audiod's software gain
    static int[] read() {
        AudioManager a = audio;
        if (a == null || a.isVolumeFixed())
            return null;
        int min = a.getStreamMinVolume(AudioManager.STREAM_MUSIC);
        int max = a.getStreamMaxVolume(AudioManager.STREAM_MUSIC);
        int index = a.getStreamVolume(AudioManager.STREAM_MUSIC);
        return new int[] {index - min, max - min, a.isStreamMute(AudioManager.STREAM_MUSIC) ? 1 : 0};
    }

    // from audiod's thread: the index (offset by the minimum) cadenced's level maps to. no flag: android shows no
    // panel of its own, the screen shows cadence's overlay
    static boolean write(int index) {
        AudioManager a = audio;
        if (a == null)
            return false;
        try {
            a.setStreamVolume(AudioManager.STREAM_MUSIC, index + a.getStreamMinVolume(AudioManager.STREAM_MUSIC), 0);
            return true;
        } catch (SecurityException e) {
            // do not disturb refuses a change that would unmute
            Log.w(TAG, "system_volume_refused");
            return false;
        }
    }
}

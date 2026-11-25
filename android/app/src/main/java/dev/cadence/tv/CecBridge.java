package dev.cadence.tv;

import android.content.Context;
import android.media.AudioManager;
import android.net.LocalServerSocket;
import android.net.LocalSocket;
import android.provider.Settings;
import android.util.Log;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.TimeUnit;

// the app's side of display.cec (device/internal/cec's Bridge on cadenced's): the framework owns hdmi-cec and
// keeps its api from an app (HdmiControlService's binder checks the signature-level HDMI_CEC on every call),
// so this serves what an app may do on a socket instead — the volume keys AudioService itself forwards as
// cec while the stream volume is the tv's, and the shell keys (HOME's one touch play, SLEEP's standby) the
// owner's adbd pairing runs — one json line in, one out. logs carry codes alone
final class CecBridge implements Runnable {
    private static final String TAG = "cadence-cec";
    // the abstract name cadenced dials (device/boards/android-tv)
    private static final String NAME = "cadence-cec";
    private static final long STATUS_EVERY_MS = 25_000;
    private static final long KEY_TIMEOUT_MS = 5_000;

    private final Context context;
    private final AudioManager audio;
    private final AdbShell shell;
    private LocalServerSocket server;
    private Thread thread;
    private String tvPower = "unknown";
    private long statusAt;

    CecBridge(Context context) {
        this.context = context;
        this.audio = context.getSystemService(AudioManager.class);
        this.shell = new AdbShell(context);
    }

    void start() {
        try {
            server = new LocalServerSocket(NAME);
        } catch (Exception e) {
            Log.e(TAG, "cec_socket_failed");
            return;
        }
        thread = new Thread(this, "cadence-cec");
        thread.start();
    }

    void stop() {
        if (server != null) {
            try {
                server.close();
            } catch (Exception ignored) {
            }
        }
        if (thread != null)
            thread.interrupt();
    }

    @Override
    public void run() {
        while (!Thread.currentThread().isInterrupted()) {
            try (LocalSocket c = server.accept()) {
                BufferedReader in = new BufferedReader(new InputStreamReader(c.getInputStream(), StandardCharsets.UTF_8));
                String line = in.readLine();
                if (line == null)
                    continue;
                JSONObject answer = handle(new JSONObject(line));
                OutputStream out = c.getOutputStream();
                out.write((answer.toString() + "\n").getBytes(StandardCharsets.UTF_8));
                out.flush();
            } catch (Exception | LinkageError e) {
                // a closed server is stop; a broken client is one refused call; a LinkageError (a method the
                // device's android lacks) must not escape either — this thread shares the daemon process with
                // cadenced, and an uncaught one kills it
                if (Thread.currentThread().isInterrupted())
                    return;
            }
        }
    }

    private JSONObject handle(JSONObject req) {
        String op = req.optString("op", "");
        switch (op) {
            case "info":
                return info();
            case "volume":
                return volume(req.optString("action", ""));
            case "screen":
                return screen(req.optBoolean("on", false));
        }
        return fail("unknown_op");
    }

    // present: the framework's cec is on (its default); fixedVolume: the stream volume is the tv's own, the
    // one case AudioService forwards adjusts as cec keys; shell: adbd answers on loopback; tvPower: what its
    // own dumpsys said, refreshed at the cadence cadenced asks
    private JSONObject info() {
        refreshStatus();
        JSONObject o = new JSONObject();
        try {
            boolean enabled = Settings.Global.getInt(
                context.getContentResolver(), "hdmi_control_enabled", 1) != 0;
            o.put("present", enabled && audio != null);
            o.put("fixedVolume", audio != null && audio.isVolumeFixed());
            o.put("shell", AdbShell.listening());
            o.put("tvPower", tvPower);
            o.put("systemAudio", "unknown");
        } catch (Exception e) {
            return fail("unavailable");
        }
        return o;
    }

    private JSONObject volume(String action) {
        int dir;
        switch (action) {
            case "raise":
                dir = AudioManager.ADJUST_RAISE;
                break;
            case "lower":
                dir = AudioManager.ADJUST_LOWER;
                break;
            case "mute":
                dir = AudioManager.ADJUST_TOGGLE_MUTE;
                break;
            default:
                return fail("invalid");
        }
        if (audio == null)
            return fail("unavailable");
        // a stream volume the stick itself owns is cadence's own level (audio.output): not ours to double
        if (!audio.isVolumeFixed())
            return fail("not_fixed");
        // the one public api on the path: while the tv holds the volume, AudioService turns the adjust into
        // the cec key pair itself (the avr's while system audio is on, else the tv's)
        try {
            audio.adjustStreamVolume(AudioManager.STREAM_MUSIC, dir, AudioManager.FLAG_SHOW_UI);
        } catch (SecurityException e) {
            // do not disturb refuses a change that would unmute
            return fail("refused");
        }
        return ok();
    }

    private JSONObject screen(boolean on) {
        // one touch play is the home key (it wakes and takes the source in one), standby the sleep key; both
        // need the shell only the owner's adb pairing gives this app
        boolean done;
        if (on) {
            done = shell.run("input keyevent 224", KEY_TIMEOUT_MS)
                && shell.run("input keyevent 3", KEY_TIMEOUT_MS);
        } else {
            done = shell.run("input keyevent 223", KEY_TIMEOUT_MS);
        }
        if (done)
            tvPower = on ? "on" : "standby";
        return done ? ok() : fail("no_shell");
    }

    // refreshStatus rereads the framework's own word for the tv's power through its dumpsys, at most once a
    // cadence: mPowerStatus's operand (0 on, 1 standby, 2 in transition to on, 3 to standby), as hdmi_control
    // dumps it on this level
    private void refreshStatus() {
        long now = System.nanoTime();
        if (TimeUnit.NANOSECONDS.toMillis(now - statusAt) < STATUS_EVERY_MS)
            return;
        statusAt = now;
        if (!AdbShell.listening())
            return;
        StringBuilder out = new StringBuilder();
        if (!shell.run(out, "dumpsys hdmi_control", KEY_TIMEOUT_MS)) {
            tvPower = "unknown";
            return;
        }
        for (String line : out.toString().split("\n")) {
            line = line.trim();
            if (!line.startsWith("mPowerStatus="))
                continue;
            String v = line.substring("mPowerStatus=".length()).trim();
            if (v.startsWith("0") || v.contains("on"))
                tvPower = v.contains("transition") ? "to_on" : "on";
            else if (v.startsWith("1") || v.contains("standby"))
                tvPower = "standby";
            else if (v.startsWith("2"))
                tvPower = "to_on";
            else if (v.startsWith("3"))
                tvPower = "to_off";
            else
                tvPower = "unknown";
            return;
        }
        tvPower = "unknown";
    }

    private static JSONObject ok() {
        try {
            return new JSONObject().put("ok", true);
        } catch (org.json.JSONException e) {
            return new JSONObject();
        }
    }

    private static JSONObject fail(String code) {
        try {
            return new JSONObject().put("ok", false).put("code", code);
        } catch (org.json.JSONException e) {
            return new JSONObject();
        }
    }
}

package dev.cadence.tv;

import android.content.Context;

import java.io.File;

// where the app keeps cadenced's state: files/cadence is cadenced's data dir (config.json, the level, the sync
// store); run/ inside it holds what cadenced hands the app (discovery.json) and the app's own pid file
final class Paths {
    private Paths() {}

    // the abstract socket names cadenced (device/boards/android-tv) and cadence-screen agree on
    static final String AUDIOD_SOCKET = "@cadence-audiod";

    static File data(Context c) {
        return new File(c.getFilesDir(), "cadence");
    }

    static File config(Context c) {
        return new File(data(c), "config.json");
    }

    // the server's url for the first run's claim, one line the owner writes over adb (run-as: android/README.md);
    // read only while no config.json exists
    static File claimServer(Context c) {
        return new File(data(c), "server");
    }

    static File run(Context c) {
        return new File(data(c), "run");
    }

    // the ca bundle written from the system's certificates (CaBundle): soloist's cdn download and spotify's own tls
    // verify against it, as a speaker's shipped bundle does
    static File caBundle(Context c) {
        return new File(data(c), "ca-bundle.crt");
    }

    // soloist's arm64 runtime as the app unpacks it from the apk's assets (SoloistRuntime): the loader audiod runs
    // spotify's build through, beside the armv7 app
    static File soloistRuntime(Context c) {
        return new File(new File(data(c), "soloist"), "runtime");
    }

    static File discovery(Context c) {
        return new File(run(c), "discovery.json");
    }

    static File cadencedPid(Context c) {
        return new File(run(c), "cadenced.pid");
    }

    static File cadenced(Context c) {
        return new File(c.getApplicationInfo().nativeLibraryDir, "libcadenced.so");
    }
}

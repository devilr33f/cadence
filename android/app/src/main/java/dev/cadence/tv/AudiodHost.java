package dev.cadence.tv;

// audiod's engine in the daemon process (libcadence-daemon.so): the daemon cadence-audiod would be, on a native thread,
// listening on an abstract socket cadenced dials; its json log lines go to logcat under the tag cadence-audiod. the
// dnssd bridge for libairplay's records lives in the same library (NsdBridge)
final class AudiodHost {
    static {
        System.loadLibrary("cadence-daemon");
    }

    private AudiodHost() {}

    // true when it listens (or already did)
    static native boolean nativeStart(String socket);

    // stops it and waits for its thread
    static native void nativeStop();

    // false once its loop ended by itself; the service starts it again
    static native boolean nativeRunning();
}

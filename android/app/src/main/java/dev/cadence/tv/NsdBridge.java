package dev.cadence.tv;

import android.content.Context;
import android.net.nsd.NsdManager;
import android.net.nsd.NsdServiceInfo;
import android.os.FileObserver;
import android.util.Log;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.File;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.HashMap;
import java.util.Iterator;
import java.util.Map;

// the app's mdns: android answers 5353 itself, so services are registered with NsdManager instead of a responder of
// our own. two sources, each keyed by its service type:
//   - cadenced's _cadence._tcp: cadenced writes run/discovery.json (its instance name and txt keys, device/internal/
//     discovery's Handoff) while network.discovery advertises and removes it when it stops; NsdManager wants a port,
//     and cadenced listens on none, so the record says 9 (discard), which browsers ignore as they do cadenced's 0
//   - libairplay's _raop._tcp and _airplay._tcp: its dnssd hands its records to the backend the daemon library makes
//     (cadence_android_dnssd_backend), which calls registerFromNative and unregisterFromNative here
// logs carry service types and codes alone, never names or records
final class NsdBridge {
    private static final String TAG = "cadence-nsd";
    private static final String CADENCE = "_cadence._tcp";
    private static final int CADENCE_PORT = 9;

    private static volatile NsdBridge current;

    private final Context context;
    private final NsdManager nsd;
    private final Map<String, NsdManager.RegistrationListener> registered = new HashMap<>();
    private FileObserver observer;

    NsdBridge(Context context) {
        this.context = context;
        this.nsd = context.getSystemService(NsdManager.class);
    }

    void start() {
        current = this;
        File run = Paths.run(context);
        observer = new FileObserver(run, FileObserver.CLOSE_WRITE | FileObserver.MOVED_TO | FileObserver.DELETE
            | FileObserver.MOVED_FROM) {
            @Override
            public void onEvent(int event, String path) {
                if ("discovery.json".equals(path))
                    syncCadence();
            }
        };
        observer.startWatching();
        syncCadence();
    }

    void stop() {
        current = null;
        if (observer != null)
            observer.stopWatching();
        synchronized (this) {
            for (NsdManager.RegistrationListener l : registered.values())
                unregister(l);
            registered.clear();
        }
    }

    private void syncCadence() {
        File f = Paths.discovery(context);
        if (!f.isFile()) {
            withdraw(CADENCE);
            return;
        }
        try {
            JSONObject o = new JSONObject(new String(Files.readAllBytes(f.toPath()), StandardCharsets.UTF_8));
            JSONObject txt = o.getJSONObject("txt");
            Map<String, String> attrs = new HashMap<>();
            for (Iterator<String> it = txt.keys(); it.hasNext(); ) {
                String k = it.next();
                attrs.put(k, txt.getString(k));
            }
            advertise(CADENCE, o.getString("name"), CADENCE_PORT, attrs);
        } catch (IOException | JSONException e) {
            // replaced while read: the rename's event follows
            Log.w(TAG, "handoff_unreadable type=" + CADENCE);
        }
    }

    private synchronized void advertise(String type, String name, int port, Map<String, String> attrs) {
        withdraw(type);
        NsdServiceInfo info = new NsdServiceInfo();
        info.setServiceType(type);
        info.setServiceName(name);
        info.setPort(port);
        for (Map.Entry<String, String> e : attrs.entrySet())
            info.setAttribute(e.getKey(), e.getValue());
        NsdManager.RegistrationListener l = new NsdManager.RegistrationListener() {
            @Override
            public void onServiceRegistered(NsdServiceInfo s) {
                Log.i(TAG, "registered type=" + type);
            }

            @Override
            public void onRegistrationFailed(NsdServiceInfo s, int code) {
                Log.w(TAG, "register_failed type=" + type + " code=" + code);
            }

            @Override
            public void onServiceUnregistered(NsdServiceInfo s) {
                Log.i(TAG, "unregistered type=" + type);
            }

            @Override
            public void onUnregistrationFailed(NsdServiceInfo s, int code) {
                Log.w(TAG, "unregister_failed type=" + type + " code=" + code);
            }
        };
        try {
            nsd.registerService(info, NsdManager.PROTOCOL_DNS_SD, l);
            registered.put(type, l);
        } catch (IllegalArgumentException e) {
            Log.w(TAG, "register_refused type=" + type);
        }
    }

    private synchronized void withdraw(String type) {
        NsdManager.RegistrationListener l = registered.remove(type);
        if (l != null)
            unregister(l);
    }

    private void unregister(NsdManager.RegistrationListener l) {
        try {
            nsd.unregisterService(l);
        } catch (IllegalArgumentException ignored) {
            // never registered (its registration failed)
        }
    }

    // libairplay's backend (libcadence-daemon.so, dnssd-jni.cpp): txt in rfc 6763 wire format, a length byte and
    // key=value per entry. 0, or a negative errno
    static int registerFromNative(String type, String name, int port, byte[] txt) {
        NsdBridge b = current;
        if (b == null)
            return -19; // ENODEV: the service is not up
        Map<String, String> attrs = new HashMap<>();
        for (int i = 0; i < txt.length; ) {
            int n = txt[i] & 0xff;
            if (i + 1 + n > txt.length)
                return -22; // EINVAL
            String entry = new String(txt, i + 1, n, StandardCharsets.UTF_8);
            int eq = entry.indexOf('=');
            if (eq > 0)
                attrs.put(entry.substring(0, eq), entry.substring(eq + 1));
            else if (!entry.isEmpty())
                attrs.put(entry, null);
            i += 1 + n;
        }
        b.advertise(type, name, port, attrs);
        return 0;
    }

    static void unregisterFromNative(String type) {
        NsdBridge b = current;
        if (b != null)
            b.withdraw(type);
    }
}

package dev.cadence.tv;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
import android.os.SystemClock;
import android.util.Log;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.Map;
import java.util.concurrent.TimeUnit;

// the ":daemon" process: audiod's engine in process (AudiodHost), cadenced as a child (S2: a GOOS=android build from
// the native library dir, DNS through bionic, the system's CA pool), and the NsdManager bridge. a foreground service,
// so android keeps the process and its child running with the screen in the background; START_STICKY brings it back
// after the system kills it. cadenced is restarted with a backoff while the service lives, and a cadenced a killed
// process left behind (its pid kept in run/) is stopped before the next starts, so two never run
public final class DaemonService extends Service {
    private static final String TAG = "cadence-daemon";
    private static final String CHANNEL = "cadence";
    private static final int NOTIFICATION = 1;
    private static final long BACKOFF_MIN_MS = 1_000;
    private static final long BACKOFF_MAX_MS = 60_000;
    // a run this long starts the backoff over
    private static final long BACKOFF_RESET_MS = 60_000;

    private volatile boolean stopping;
    private volatile Process child;
    private Thread supervisor;
    private PowerManager.WakeLock wakeLock;
    private WifiManager.WifiLock wifiLock;
    private WifiManager.MulticastLock multicastLock;
    private NsdBridge nsd;
    private SystemVolume volume;
    private CecBridge cec;

    static void start(Context c) {
        c.startForegroundService(new Intent(c, DaemonService.class));
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    @Override
    public void onCreate() {
        super.onCreate();
        NotificationManager nm = getSystemService(NotificationManager.class);
        nm.createNotificationChannel(
            new NotificationChannel(CHANNEL, getString(R.string.channel_name), NotificationManager.IMPORTANCE_MIN));
        Notification n = new Notification.Builder(this, CHANNEL)
            .setContentTitle(getString(R.string.running))
            .setSmallIcon(android.R.drawable.stat_notify_sync)
            .setOngoing(true)
            .build();
        startForeground(NOTIFICATION, n);

        // the cpu and wi-fi stay up for the link and the audio; the multicast lock lets the lan's mdns traffic reach
        // the app's sockets (libairplay's peers resolve the stick through the system's responder either way)
        wakeLock = getSystemService(PowerManager.class).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "cadence:daemon");
        wakeLock.acquire();
        WifiManager wifi = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
        wifiLock = wifi.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "cadence:daemon");
        wifiLock.acquire();
        multicastLock = wifi.createMulticastLock("cadence:mdns");
        multicastLock.setReferenceCounted(false);
        multicastLock.acquire();

        File run = Paths.run(this);
        if (!run.isDirectory() && !run.mkdirs())
            Log.w(TAG, "run_dir_failed");
        // before audiod starts: soloist's runtime and the ca bundle are its to use (spotify's build runs through
        // the runtime's loader, against the bundle)
        CaBundle.ensure(this);
        SoloistRuntime.extract(this);
        nsd = new NsdBridge(this);
        nsd.start();
        // display.cec's bridge: the framework's cec as an app may reach it, for cadenced to dial
        cec = new CecBridge(this);
        cec.start();
        // before audiod: its Configure reads the stream
        volume = new SystemVolume(this);
        volume.start();
        if (!AudiodHost.nativeStart(Paths.AUDIOD_SOCKET))
            Log.e(TAG, "audiod_start_failed");
        supervisor = new Thread(this::superviseCadenced, "cadence-supervisor");
        supervisor.start();
        Log.i(TAG, "daemon_started");
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        return START_STICKY;
    }

    @Override
    public void onDestroy() {
        stopping = true;
        if (supervisor != null)
            supervisor.interrupt();
        stopChild(child);
        try {
            if (supervisor != null)
                supervisor.join(5_000);
        } catch (InterruptedException ignored) {
            Thread.currentThread().interrupt();
        }
        // after cadenced: it dials audiod until it exits
        AudiodHost.nativeStop();
        volume.stop();
        nsd.stop();
        if (cec != null)
            cec.stop();
        if (multicastLock.isHeld())
            multicastLock.release();
        if (wifiLock.isHeld())
            wifiLock.release();
        if (wakeLock.isHeld())
            wakeLock.release();
        Log.i(TAG, "daemon_stopped");
        super.onDestroy();
    }

    private void superviseCadenced() {
        stopStale();
        long delay = BACKOFF_MIN_MS;
        while (!stopping) {
            if (!AudiodHost.nativeRunning() && !AudiodHost.nativeStart(Paths.AUDIOD_SOCKET))
                Log.e(TAG, "audiod_restart_failed");
            long started = SystemClock.elapsedRealtime();
            int code = runCadenced();
            if (stopping)
                break;
            long ran = SystemClock.elapsedRealtime() - started;
            Log.w(TAG, "cadenced_exit code=" + code + " ran_ms=" + ran);
            if (ran >= BACKOFF_RESET_MS)
                delay = BACKOFF_MIN_MS;
            try {
                Thread.sleep(delay);
            } catch (InterruptedException e) {
                break;
            }
            delay = Math.min(delay * 2, BACKOFF_MAX_MS);
        }
    }

    // one run of cadenced, its stdout and stderr (json lines: ids, codes and timings alone) to logcat. enrolled (a
    // config.json), it runs; not yet, with the owner's server url in files/cadence/server, it claims: `cadenced
    // claim` shows the code and qr on the screen and writes config.json once the owner approves it in the dashboard,
    // then exits 0 and the next run is the daemon. with neither, it waits for config.json (an enroll over adb), and is
    // stopped to claim as soon as the server's url appears
    private int runCadenced() {
        File data = Paths.data(this);
        if (!data.isDirectory() && !data.mkdirs())
            Log.w(TAG, "data_dir_failed");
        String cadenced = Paths.cadenced(this).getAbsolutePath();
        String config = Paths.config(this).getAbsolutePath();
        String server = Paths.config(this).isFile() ? null : claimServer();
        ProcessBuilder pb = server != null
            ? new ProcessBuilder(cadenced, "claim", "--server", server, "--board", "android-tv", "--config", config,
                "--data-dir", data.getAbsolutePath())
            : new ProcessBuilder(cadenced, "--config", config, "--wait-config");
        pb.directory(data).redirectErrorStream(true);
        Log.i(TAG, server != null ? "cadenced_claim" : "cadenced_start");
        Map<String, String> env = pb.environment();
        env.clear();
        env.put("HOME", getFilesDir().getAbsolutePath());
        env.put("TMPDIR", getCacheDir().getAbsolutePath());
        // the board trusts the app's word (an app reads no device tree, S2): device/boards/android-tv
        env.put("CADENCE_ANDROID_TV", "1");
        env.put("CADENCE_ANDROID_FINGERPRINT", Build.FINGERPRINT);
        Process p;
        try {
            p = pb.start();
        } catch (IOException e) {
            Log.e(TAG, "cadenced_spawn_failed");
            return -1;
        }
        child = p;
        writePid(p);
        Thread watcher = null;
        if (server == null && !Paths.config(this).isFile()) {
            // waiting for a config: a server url written meanwhile turns this run into a claim
            watcher = new Thread(() -> {
                while (!Thread.currentThread().isInterrupted() && p.isAlive()) {
                    if (!Paths.config(this).isFile() && claimServer() != null) {
                        Log.i(TAG, "claim_server_set");
                        stopChild(p);
                        return;
                    }
                    SystemClock.sleep(2_000);
                }
            }, "cadence-claim-watch");
            watcher.start();
        }
        try (BufferedReader r = new BufferedReader(new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8))) {
            String line;
            while ((line = r.readLine()) != null)
                Log.i("cadenced", line);
        } catch (IOException ignored) {
            // the pipe closes as cadenced exits
        }
        try {
            return p.waitFor();
        } catch (InterruptedException e) {
            stopChild(p);
            return -1;
        } finally {
            if (watcher != null)
                watcher.interrupt();
            child = null;
            //noinspection ResultOfMethodCallIgnored
            Paths.cadencedPid(this).delete();
        }
    }

    // the claim's server url (files/cadence/server: one http or https url), or null; never logged
    private String claimServer() {
        File f = Paths.claimServer(this);
        if (!f.isFile() || f.length() > 2048)
            return null;
        try {
            String url = new String(Files.readAllBytes(f.toPath()), StandardCharsets.UTF_8).trim();
            if ((url.startsWith("https://") || url.startsWith("http://")) && url.indexOf(' ') < 0 && url.indexOf('\n') < 0)
                return url;
            Log.w(TAG, "claim_server_invalid");
        } catch (IOException ignored) {
            // unreadable: as if absent
        }
        return null;
    }

    // SIGTERM, then SIGKILL after cadenced's own grace (it stops its links and the screen plane in order)
    private static void stopChild(Process p) {
        if (p == null)
            return;
        int pid = pidOf(p);
        if (pid > 0)
            android.os.Process.sendSignal(pid, 15);
        else
            p.destroy();
        try {
            if (!p.waitFor(5, TimeUnit.SECONDS))
                p.destroyForcibly();
        } catch (InterruptedException e) {
            p.destroyForcibly();
            Thread.currentThread().interrupt();
        }
    }

    private void writePid(Process p) {
        int pid = pidOf(p);
        if (pid <= 0)
            return;
        try (FileOutputStream out = new FileOutputStream(Paths.cadencedPid(this))) {
            out.write(Integer.toString(pid).getBytes(StandardCharsets.US_ASCII));
        } catch (IOException e) {
            Log.w(TAG, "pid_write_failed");
        }
    }

    // a cadenced the previous process left (it was killed, its child lived on): stopped if it is still ours
    private void stopStale() {
        File f = Paths.cadencedPid(this);
        if (!f.isFile())
            return;
        try {
            int pid = Integer.parseInt(new String(Files.readAllBytes(f.toPath()), StandardCharsets.US_ASCII).trim());
            String cmdline = new String(Files.readAllBytes(new File("/proc/" + pid + "/cmdline").toPath()),
                StandardCharsets.UTF_8);
            if (cmdline.startsWith(Paths.cadenced(this).getAbsolutePath())) {
                android.os.Process.sendSignal(pid, 15);
                for (int i = 0; i < 50 && new File("/proc/" + pid).exists(); i++)
                    SystemClock.sleep(100);
                if (new File("/proc/" + pid).exists())
                    android.os.Process.sendSignal(pid, 9);
                Log.w(TAG, "cadenced_stale_stopped");
            }
        } catch (IOException | NumberFormatException ignored) {
            // gone already, or not ours
        }
        //noinspection ResultOfMethodCallIgnored
        f.delete();
    }

    // android 11's libcore has no Process.pid(): its UNIXProcess keeps it in a field (S2)
    private static int pidOf(Process p) {
        try {
            java.lang.reflect.Field f = p.getClass().getDeclaredField("pid");
            f.setAccessible(true);
            return f.getInt(p);
        } catch (ReflectiveOperationException | RuntimeException e) {
            return -1;
        }
    }
}

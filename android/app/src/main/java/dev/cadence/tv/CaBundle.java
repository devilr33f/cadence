package dev.cadence.tv;

import android.content.Context;
import android.util.Log;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.Arrays;

// the system's ca certificates as one bundle at files/cadence/ca-bundle.crt (Paths.caBundle): android's store is a
// directory of single-cert files no glibc binary reads, and soloist's arm64 runtime verifies spotify's cdn and its
// own tls against one file, as a speaker's shipped bundle does. rewritten when the store's bytes changed, so a
// system update brings its certificates; unreadable or empty leaves the last bundle in place (cadenced and audiod
// report ca_missing rather than run soloist against nothing)
final class CaBundle {
    private CaBundle() {}

    static void ensure(Context c) {
        File out = Paths.caBundle(c);
        try {
            byte[] bundle = readStore();
            if (bundle.length == 0)
                return;
            if (out.isFile() && Arrays.equals(bundle, read(new FileInputStream(out))))
                return;
            File parent = out.getParentFile();
            // the data dir may not exist on the first start (cadenced makes it later): make it, or soloist waits
            // for a bundle until the next service start
            if (!parent.isDirectory() && !parent.mkdirs())
                throw new IOException("mkdir");
            File fresh = new File(parent, "ca-bundle.crt.new");
            write(fresh, bundle);
            if (!fresh.renameTo(out))
                throw new IOException("rename");
        } catch (IOException e) {
            Log.w("cadence-daemon", "ca_bundle_failed");
        }
    }

    // the store's pem files in name order, concatenated: one certificate each, their bytes as the system wrote them
    private static byte[] readStore() throws IOException {
        File store = new File("/system/etc/security/cacerts");
        String[] names = store.list();
        if (names == null)
            return new byte[0];
        Arrays.sort(names);
        ByteArrayOutputStream out = new ByteArrayOutputStream(256 * 1024);
        for (String name : names) {
            if (!name.endsWith(".0"))
                continue;
            File cert = new File(store, name);
            byte[] pem = read(new FileInputStream(cert));
            if (pem.length == 0 || pem[0] != '-')
                continue;
            out.write(pem, 0, pem.length);
            if (pem[pem.length - 1] != '\n')
                out.write('\n');
        }
        return out.toByteArray();
    }

    private static byte[] read(InputStream in) throws IOException {
        // InputStream.readAllBytes is api 33
        try (InputStream s = in; ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] buffer = new byte[8 * 1024];
            for (int n; (n = s.read(buffer)) > 0;)
                out.write(buffer, 0, n);
            return out.toByteArray();
        }
    }

    private static void write(File f, byte[] data) throws IOException {
        try (OutputStream out = new FileOutputStream(f)) {
            out.write(data);
        }
    }
}

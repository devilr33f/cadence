package dev.cadence.tv;

import android.content.Context;
import android.content.res.AssetManager;
import android.util.Log;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.Arrays;

// soloist's arm64 runtime (the apk's assets/soloist-runtime, as deploy/android/Dockerfile packs the prebuilt tar)
// unpacked to files/cadence/soloist/runtime (Paths.soloistRuntime), where audiod runs spotify's arm64 build through
// the runtime's loader beside the armv7 app. copied again only when the apk's manifest (the sha256 of each file)
// differs from the copy's, so an update brings its own; the loader is left executable, as assets carry no modes
final class SoloistRuntime {
    private SoloistRuntime() {}

    static void extract(Context c) {
        AssetManager assets = c.getAssets();
        File dir = Paths.soloistRuntime(c);
        File stamp = new File(dir, ".manifest");
        try {
            byte[] manifest = read(assets.open("soloist-runtime/MANIFEST"));
            if (stamp.isFile() && Arrays.equals(manifest, read(new FileInputStream(stamp))))
                return;
            File fresh = new File(dir.getParentFile(), "runtime.new");
            delete(fresh);
            copy(assets, "soloist-runtime", fresh);
            write(new File(fresh, ".manifest"), manifest);
            delete(dir);
            if (!fresh.renameTo(dir))
                throw new IOException("rename");
            setExecutable(dir);
        } catch (IOException e) {
            // audiod then reports runtime_missing, and the dashboard shows soloist failed rather than off
            Log.w("cadence-daemon", "soloist_runtime_failed");
        }
    }

    private static void copy(AssetManager assets, String path, File to) throws IOException {
        String[] children = assets.list(path);
        if (children == null)
            throw new IOException("list " + path);
        if (children.length > 0) {
            if (!to.isDirectory() && !to.mkdirs())
                throw new IOException("mkdir");
            for (String child : children)
                copy(assets, path + "/" + child, new File(to, child));
            return;
        }
        File parent = to.getParentFile();
        if (!parent.isDirectory() && !parent.mkdirs())
            throw new IOException("mkdir");
        // a leaf with no children is a file or an empty directory (the jail's bind points, which audiod still
        // checks are there): open() tells them apart, as listing cannot
        try (InputStream in = assets.open(path); OutputStream out = new FileOutputStream(to)) {
            byte[] buffer = new byte[64 * 1024];
            for (int n; (n = in.read(buffer)) > 0;)
                out.write(buffer, 0, n);
        } catch (IOException e) {
            if (!to.isDirectory() && !to.mkdir())
                throw e;
        }
    }

    // assets carry no modes: the loaders audiod execs want the exec bit, everything else the read it was given
    private static void setExecutable(File dir) {
        File[] children = dir.listFiles();
        if (children == null)
            return;
        for (File child : children) {
            if (child.isDirectory())
                setExecutable(child);
            else if (child.getName().startsWith("ld-linux"))
                //noinspection ResultOfMethodCallIgnored
                child.setExecutable(true, false);
        }
    }

    private static byte[] read(InputStream in) throws IOException {
        // InputStream.readAllBytes is api 33
        try (InputStream s = in; java.io.ByteArrayOutputStream out = new java.io.ByteArrayOutputStream()) {
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

    private static void delete(File f) {
        File[] children = f.listFiles();
        if (children != null)
            for (File child : children)
                delete(child);
        f.delete();
    }
}

package dev.cadence.tv;

import android.content.Context;
import android.content.res.AssetManager;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.Arrays;

// the screen's ui assets (the expanded rcss and the fonts, assets/ui in the apk) as files in files/ui, where
// android-main.cpp points the ui's file interface (CADENCE_SCREEN_UI_ASSETS), and the visualizer's milkdrop presets
// (assets/viz) in files/viz (CADENCE_SCREEN_MILKDROP: files/viz/milkdrop, which projectM reads by path). copied
// again only when the apk's MANIFEST (both trees' hashes) differs from the copy's, so an update brings its own;
// before sdl's thread starts
final class UiAssets {
    private UiAssets() {}

    static File dir(Context c) {
        return new File(c.getFilesDir(), "ui");
    }

    static void extract(Context c) {
        AssetManager assets = c.getAssets();
        File dir = dir(c);
        File stamp = new File(dir, ".manifest");
        try {
            byte[] manifest = read(assets.open("MANIFEST"));
            if (stamp.isFile() && Arrays.equals(manifest, read(new FileInputStream(stamp))))
                return;
            // viz first: the stamp lives in ui, so a copy cut short is made again at the next start
            replace(c, assets, "viz");
            File fresh = new File(c.getFilesDir(), "ui.new");
            delete(fresh);
            copy(assets, "ui", fresh);
            write(new File(fresh, ".manifest"), manifest);
            delete(dir);
            if (!fresh.renameTo(dir))
                throw new IOException("rename");
        } catch (IOException e) {
            // the ui then starts with what it finds (an older copy, or nothing: it shows no text)
            android.util.Log.w("cadence-screen", "ui_assets_failed");
        }
    }

    // files/<name> made afresh from the apk's assets/<name>, or removed when the apk has none
    private static void replace(Context c, AssetManager assets, String name) throws IOException {
        File dir = new File(c.getFilesDir(), name);
        File fresh = new File(c.getFilesDir(), name + ".new");
        delete(fresh);
        String[] children = assets.list(name);
        if (children == null || children.length == 0) {
            delete(dir);
            return;
        }
        copy(assets, name, fresh);
        delete(dir);
        if (!fresh.renameTo(dir))
            throw new IOException("rename");
    }

    private static void copy(AssetManager assets, String path, File to) throws IOException {
        String[] children = assets.list(path);
        if (children != null && children.length > 0) {
            if (!to.isDirectory() && !to.mkdirs())
                throw new IOException("mkdir");
            for (String child : children)
                copy(assets, path + "/" + child, new File(to, child));
            return;
        }
        try (InputStream in = assets.open(path); OutputStream out = new FileOutputStream(to)) {
            byte[] buffer = new byte[64 * 1024];
            for (int n; (n = in.read(buffer)) > 0;)
                out.write(buffer, 0, n);
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

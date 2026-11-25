package dev.cadence.tv;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.SocketTimeoutException;
import java.net.URL;

import javax.net.ssl.SSLException;

// cadence-screen's https for the server's image proxy (screen/ui/images' Transport, android-main.cpp calls it on the
// image cache's worker threads): HttpURLConnection, so trust is the system's CA store and its updates, as cadenced's
// own link has it. no credentials (an image id is the capability), no redirects, no cache, and nothing logged: the
// url holds the id
final class ImageFetch {
    private ImageFetch() {}

    // status[0] is set to what happened: the http status, or IO, TLS, TIMEOUT, TOO_LARGE
    static final int IO = -1;
    static final int TLS = -2;
    static final int TIMEOUT = -3;
    static final int TOO_LARGE = -4;

    // the body of a 200 of at most maxBytes, else null
    static byte[] get(String url, int maxBytes, int timeoutMs, int[] status) {
        HttpURLConnection c = null;
        try {
            c = (HttpURLConnection) new URL(url).openConnection();
            c.setConnectTimeout(timeoutMs);
            c.setReadTimeout(timeoutMs);
            c.setInstanceFollowRedirects(false);
            c.setUseCaches(false);
            // the decoder's types alone (screen/ui/images' kAccept): the server converts a webp or a gif
            c.setRequestProperty("Accept", "image/jpeg, image/png");
            c.setRequestProperty("User-Agent", "cadence-screen");
            int code = c.getResponseCode();
            status[0] = code;
            if (code != 200)
                return null;
            long length = c.getContentLengthLong();
            if (length > maxBytes) {
                status[0] = TOO_LARGE;
                return null;
            }
            ByteArrayOutputStream out = new ByteArrayOutputStream(length > 0 ? (int) length : 64 * 1024);
            byte[] buffer = new byte[16 * 1024];
            try (InputStream in = c.getInputStream()) {
                for (int n; (n = in.read(buffer)) > 0;) {
                    if (out.size() + n > maxBytes) {
                        status[0] = TOO_LARGE;
                        return null;
                    }
                    out.write(buffer, 0, n);
                }
            }
            return out.toByteArray();
        } catch (SSLException e) {
            status[0] = TLS;
        } catch (SocketTimeoutException e) {
            status[0] = TIMEOUT;
        } catch (IOException | ClassCastException | IllegalArgumentException e) {
            status[0] = IO;
        } finally {
            if (c != null)
                c.disconnect();
        }
        return null;
    }
}

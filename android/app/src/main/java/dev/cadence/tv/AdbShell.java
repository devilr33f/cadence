package dev.cadence.tv;

import android.content.Context;
import android.util.Base64;
import android.util.Log;

import java.io.ByteArrayOutputStream;
import java.io.DataInputStream;
import java.io.DataOutputStream;
import java.io.EOFException;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.math.BigInteger;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.GeneralSecurityException;
import java.security.Signature;
import java.security.interfaces.RSAPublicKey;
import java.security.spec.PKCS8EncodedKeySpec;
import java.security.spec.X509EncodedKeySpec;
import java.util.Arrays;
import java.util.concurrent.TimeUnit;
import java.util.zip.CRC32;

// the shell keys that move the tv over cec (HOME's one touch play, SLEEP's standby) need a caller the system
// signs for: an app may not inject input. the stick's adbd, which the owner keeps on tcpip, takes our own key
// once confirmed on the tv, so this is a minimal adb client for loopback (system/core/adb/protocol.txt):
// CNXN, the RSA auth dance (SHA1withRSA over the 20-byte token, then the mincrypt-form public key the tv
// asks the owner to confirm), one shell stream per command. it holds no connection, talks to 127.0.0.1
// alone, and every failure is a refused command, never a crash of the service. a confirm dialog that stands
// unanswered backs off: the cec poll must not re-prompt on the tv every half minute
final class AdbShell {
    private static final String TAG = "cadence-cec";
    static final int PORT = 5555;
    private static final int MAX_DATA = 4096;

    // the adb packet commands as the wire spells them, with its bytes little-endian
    private static final int CMD_CNXN = 0x4e584e43;
    private static final int CMD_AUTH = 0x48545541;
    private static final int CMD_OPEN = 0x4e45504f;
    private static final int CMD_OKAY = 0x59414b4f;
    private static final int CMD_WRTE = 0x45545257;
    private static final int CMD_CLSE = 0x45534c43;
    // A_AUTH's types
    private static final int AUTH_TOKEN = 1;
    private static final int AUTH_SIGNATURE = 2;
    private static final int AUTH_RSAPUBLICKEY = 3;

    private final Context context;
    private KeyPair keys;
    private int pairingMisses;
    private long retryAtNs;

    AdbShell(Context context) {
        this.context = context;
    }

    // listening says adbd is on tcpip at all; a pairing is only proven by a command's ok
    static boolean listening() {
        try (Socket s = new Socket()) {
            s.connect(new InetSocketAddress("127.0.0.1", PORT), 300);
            return true;
        } catch (IOException e) {
            return false;
        }
    }

    // run runs one shell command, collecting its output into out (null drops it); false when adbd is off,
    // the owner has not confirmed our key (the first call shows the dialog on the tv; an unanswered one
    // backs off — a minute, then ten, then half an hour — until a command finally goes through), or the
    // stream broke
    synchronized boolean run(StringBuilder out, String command, long timeoutMs) {
        if (keys() == null || System.nanoTime() < retryAtNs)
            return false;
        int answer = exchange(out, command, timeoutMs);
        if (answer == EXCHANGED) {
            pairingMisses = 0;
            retryAtNs = 0;
            return true;
        }
        if (answer == PROMPTED) {
            long[] waits = {60_000, 600_000, 1_800_000};
            long wait = waits[Math.min(pairingMisses, waits.length - 1)];
            retryAtNs = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(wait);
            pairingMisses++;
        }
        return false;
    }

    // exchange's answers: a command done, a plain failure, or a confirm dialog that stood unanswered
    private static final int EXCHANGED = 0, BROKEN = 1, PROMPTED = 2;

    // exchange is one command's wire work
    private int exchange(StringBuilder out, String command, long timeoutMs) {
        long deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(timeoutMs);
        boolean prompted = false;
        try (Socket s = new Socket()) {
            s.connect(new InetSocketAddress("127.0.0.1", PORT), 300);
            s.setSoTimeout(300);
            Packet p = new Packet(s);
            p.write(CMD_CNXN, 0x01000000, MAX_DATA, "host::".getBytes(StandardCharsets.UTF_8));
            // auth: a token to sign, a second token to mean "unknown key" (the confirm dialog follows), then
            // CNXN, or a closed socket when the owner refused
            boolean signed = false;
            while (true) {
                int[] head = p.read(deadline);
                if (head == null)
                    return prompted ? PROMPTED : BROKEN;
                if (head[0] == CMD_CNXN)
                    break;
                if (head[0] == CMD_AUTH && head[1] == AUTH_TOKEN) {
                    if (!signed) {
                        p.write(CMD_AUTH, AUTH_SIGNATURE, 0, sign(p.payload()));
                        signed = true;
                    } else {
                        p.write(CMD_AUTH, AUTH_RSAPUBLICKEY, 0, publicKeyBytes(keys));
                        prompted = true;
                    }
                } else {
                    return BROKEN;
                }
            }
            // one shell stream: open, take its writes until it closes
            int localId = 1;
            p.write(CMD_OPEN, localId, 0, ("shell:" + command + "\0").getBytes(StandardCharsets.UTF_8));
            int remoteId = -1;
            while (true) {
                int[] head = p.read(deadline);
                if (head == null)
                    return BROKEN;
                if (head[0] == CMD_OKAY) {
                    remoteId = head[1];
                } else if (head[0] == CMD_WRTE) {
                    byte[] data = p.payload();
                    if (out != null && data.length > 0)
                        out.append(new String(data, StandardCharsets.UTF_8));
                    if (remoteId >= 0)
                        p.write(CMD_OKAY, localId, remoteId, new byte[0]);
                } else if (head[0] == CMD_CLSE) {
                    return EXCHANGED;
                }
            }
        } catch (IOException | GeneralSecurityException | LinkageError e) {
            return prompted ? PROMPTED : BROKEN;
        }
    }

    boolean run(String command, long timeoutMs) {
        return run(null, command, timeoutMs);
    }

    // sign is the adb auth signature: SHA1 with RSA (PKCS#1 v1.5) over the token
    private byte[] sign(byte[] token) throws GeneralSecurityException {
        Signature sig = Signature.getInstance("SHA1withRSA");
        sig.initSign(keys.getPrivate());
        sig.update(token);
        return sig.sign();
    }

    // publicKeyBytes is the key exactly as adb's own client sends it (system/core/adb's crypto_utils and
    // protocol.txt): base64 of mincrypt's struct — the modulus' word count, -1/n[0] mod 2^32 (montgomery),
    // the modulus and R^2 mod n little-endian, the exponent — then a space, a name and the nul. adbd saves
    // that line as it stands on "always allow", so the pairing survives the tv's reboot; the legacy
    // len-prefixed blob this used to send never verified nor loaded back ("Invalid base64 key")
    private static byte[] publicKeyBytes(KeyPair keys) {
        RSAPublicKey pub = (RSAPublicKey) keys.getPublic();
        BigInteger n = pub.getModulus();
        int len = (n.bitLength() + 7) / 8;
        BigInteger word = BigInteger.ONE.shiftLeft(32);
        ByteArrayOutputStream blob = new ByteArrayOutputStream();
        putInt32(blob, len / 4);
        long n0 = n.longValue() & 0xFFFFFFFFL;
        putInt32(blob, word.subtract(BigInteger.valueOf(n0).modInverse(word)).longValue());
        putLE(blob, unsigned(n), len);
        putLE(blob, unsigned(BigInteger.ONE.shiftLeft(8 * len * 2).mod(n)), len);
        putInt32(blob, pub.getPublicExponent().longValue());
        return (Base64.encodeToString(blob.toByteArray(), Base64.NO_WRAP) + " cadence-tv\0")
            .getBytes(StandardCharsets.UTF_8);
    }

    // unsigned is the big integer as its bytes without the java sign byte
    private static byte[] unsigned(BigInteger v) {
        byte[] b = v.toByteArray();
        return b.length > 1 && b[0] == 0 ? Arrays.copyOfRange(b, 1, b.length) : b;
    }

    // putInt32 writes one little-endian word; putLE writes a big-endian value's bytes reversed (the
    // struct's little-endian), zero-padded to len
    private static void putInt32(ByteArrayOutputStream out, long v) {
        for (int i = 0; i < 4; i++)
            out.write((int) ((v >> (8 * i)) & 0xff));
    }

    private static void putLE(ByteArrayOutputStream out, byte[] be, int len) {
        for (int i = be.length - 1; i >= 0; i--)
            out.write(be[i]);
        for (int i = be.length; i < len; i++)
            out.write(0);
    }

    // keys is our adb identity, made once and kept beside cadenced's state
    private KeyPair keys() {
        if (keys != null)
            return keys;
        File priv = new File(Paths.data(context), "adb.key");
        File pub = new File(Paths.data(context), "adb.pub");
        try {
            KeyFactory kf = KeyFactory.getInstance("RSA");
            if (priv.isFile() && pub.isFile())
                return keys = new KeyPair(
                    kf.generatePublic(new X509EncodedKeySpec(read(pub))),
                    kf.generatePrivate(new PKCS8EncodedKeySpec(read(priv))));
            KeyPairGenerator gen = KeyPairGenerator.getInstance("RSA");
            gen.initialize(2048);
            KeyPair made = gen.generateKeyPair();
            writeAll(priv, made.getPrivate().getEncoded());
            writeAll(pub, made.getPublic().getEncoded());
            return keys = made;
        } catch (IOException | GeneralSecurityException e) {
            Log.w(TAG, "adb_key_failed");
            return null;
        }
    }

    private static byte[] read(File f) throws IOException {
        // InputStream.readAllBytes is api 33; the stick's android is older, and the NoSuchMethodError killed
        // the daemon process (cadenced runs in it)
        try (FileInputStream in = new FileInputStream(f); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] buffer = new byte[8 * 1024];
            for (int n; (n = in.read(buffer)) > 0;)
                out.write(buffer, 0, n);
            return out.toByteArray();
        }
    }

    private static void writeAll(File f, byte[] b) throws IOException {
        try (FileOutputStream out = new FileOutputStream(f)) {
            out.write(b);
        }
    }

    // one adb packet on the socket: the 24-byte header (command, two args, length, crc, magic) and the
    // payload that follows it, read with the deadline on a socket that times out when quiet
    private static final class Packet {
        private final DataInputStream in;
        private final DataOutputStream out;
        private byte[] payload = new byte[0];

        Packet(Socket s) throws IOException {
            this.in = new DataInputStream(s.getInputStream());
            this.out = new DataOutputStream(s.getOutputStream());
        }

        // read answers {command, arg0, arg1, length} and keeps the payload for payload(); null once the
        // deadline passes or the stream breaks
        int[] read(long deadlineNs) throws IOException {
            while (true) {
                if (System.nanoTime() > deadlineNs)
                    return null;
                try {
                    int cmd = Integer.reverseBytes(in.readInt());
                    int arg0 = Integer.reverseBytes(in.readInt());
                    int arg1 = Integer.reverseBytes(in.readInt());
                    int len = Integer.reverseBytes(in.readInt());
                    in.readInt(); // the payload's crc, trusted on loopback
                    int magic = Integer.reverseBytes(in.readInt());
                    if (magic != (cmd ^ 0xffffffff))
                        throw new EOFException("bad adb magic");
                    byte[] data = len > MAX_DATA ? new byte[MAX_DATA] : new byte[len];
                    in.readFully(data);
                    payload = data;
                    return new int[] {cmd, arg0, arg1, len};
                } catch (java.net.SocketTimeoutException e) {
                    // quiet: nothing on the wire yet
                }
            }
        }

        byte[] payload() {
            return payload;
        }

        void write(int cmd, int arg0, int arg1, byte[] payload) throws IOException {
            CRC32 crc = new CRC32();
            crc.update(payload);
            out.writeInt(Integer.reverseBytes(cmd));
            out.writeInt(Integer.reverseBytes(arg0));
            out.writeInt(Integer.reverseBytes(arg1));
            out.writeInt(Integer.reverseBytes(payload.length));
            out.writeInt(Integer.reverseBytes((int) crc.getValue()));
            out.writeInt(Integer.reverseBytes(cmd ^ 0xffffffff));
            out.write(payload);
            out.flush();
        }
    }
}

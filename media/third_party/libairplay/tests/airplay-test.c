/* libairplay's test: the dnssd records and the backend hook, then the receiver itself on loopback: raop_init,
 * raop_init2, the httpd on an ephemeral port, and RTSP GET /info answered 200 with a binary plist (the full answer,
 * and the txt-only one a BLE discovery query asks for). advertises nothing; with the avahi backend built, also
 * creates and frees one (no daemon needed: it waits for one). exit 0 when every check holds */
#include <arpa/inet.h>
#include <netinet/in.h>
#include <plist/plist.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>

#include "cadence_dnssd.h"
#include "raop.h"
#include "logger.h"

static int failures;

static void check(int ok, const char *fmt, ...) {
    if (ok) {
        return;
    }
    va_list ap;
    va_start(ap, fmt);
    fputs("FAIL: ", stderr);
    vfprintf(stderr, fmt, ap);
    fputc('\n', stderr);
    va_end(ap);
    failures++;
}

/* the backend a test sees: what it was asked to advertise and withdraw */
typedef struct fake {
    int registers;
    int unregisters;
    char type[32];
    char name[128];
    unsigned port;
    unsigned char txt[1300];
    size_t txt_len;
} fake_t;

static int fake_register(void *ctx, const char *type, const char *name, uint16_t port, const unsigned char *txt,
                         size_t txt_len) {
    fake_t *f = ctx;
    f->registers++;
    snprintf(f->type, sizeof f->type, "%s", type);
    snprintf(f->name, sizeof f->name, "%s", name);
    f->port = port;
    memcpy(f->txt, txt, txt_len);
    f->txt_len = txt_len;
    return 0;
}

static void fake_unregister(void *ctx, const char *type) {
    fake_t *f = ctx;
    f->unregisters++;
    snprintf(f->type, sizeof f->type, "%s", type);
}

/* the records' keys in order, space-separated, and one key's value */
static void keys(const unsigned char *txt, size_t len, char *out, size_t out_len, const char *want, char *value,
                 size_t value_len) {
    size_t off = 0;
    char key[64];
    const char *v;
    size_t vl;
    int rc;
    out[0] = '\0';
    value[0] = '\0';
    while ((rc = cadence_dnssd_txt_next(txt, len, &off, key, sizeof key, &v, &vl)) == 1) {
        if (out[0]) {
            strncat(out, " ", out_len - strlen(out) - 1);
        }
        strncat(out, key, out_len - strlen(out) - 1);
        if (!strcmp(key, want) && v) {
            snprintf(value, value_len, "%.*s", (int) vl, v);
        }
    }
    check(rc == 0, "txt walk ended with %d", rc);
}

static const char hw[6] = {0x02, 0x11, 0x22, 0x33, 0x44, (char) 0xA5};

static void test_records(void) {
    int err = -1;
    dnssd_t *d = dnssd_init("cadence-test", 12, hw, sizeof hw, &err, 0);
    check(d && err == DNSSD_ERROR_NOERROR, "dnssd_init: %d", err);
    if (!d) {
        return;
    }
    check(!dnssd_init("x", 1, hw, 7, &err, 0) && err == DNSSD_ERROR_HWADDRLEN, "a 7-byte hw address accepted");

    /* no backend: the records are built, nothing is advertised */
    check(dnssd_register_raop(d, 7000) == 0, "register_raop without a backend");
    int len = 0;
    check(dnssd_get_raop_txt(d, &len) && len > 0, "no raop records");
    dnssd_unregister_raop(d);

    fake_t f = {0};
    cadence_dnssd_backend_t b = {&f, fake_register, fake_unregister};
    cadence_dnssd_set_backend(d, &b);
    dnssd_set_pk(d, "abc123");
    dnssd_set_airplay_features(d, 40, 1);
    check(dnssd_get_airplay_features(d) == ((1ull << 40) | 0x5A7FFEE6ull), "features %llx",
          (unsigned long long) dnssd_get_airplay_features(d));

    char k[512], v[64];
    check(dnssd_register_raop(d, 7000) == 0 && f.registers == 1, "register_raop");
    check(!strcmp(f.type, CADENCE_DNSSD_RAOP) && !strcmp(f.name, "0211223344A5@cadence-test") && f.port == 7000,
          "raop advertised as %s %s %u", f.type, f.name, f.port);
    keys(f.txt, f.txt_len, k, sizeof k, "ft", v, sizeof v);
    /* uxplay sets sf twice: bonjour's TXTRecordSetValue moves it after txtvers */
    check(!strcmp(k, "ch cn da et vv ft am md rhd pw sr ss sv tp txtvers sf vs vn pk"), "raop keys: %s", k);
    check(!strcmp(v, "0x5A7FFEE6,0x100"), "raop ft=%s", v);
    len = 0;
    const char *txt = dnssd_get_raop_txt(d, &len);
    check((size_t) len == f.txt_len && !memcmp(txt, f.txt, f.txt_len), "get_raop_txt differs from the advertised one");
    keys(f.txt, f.txt_len, k, sizeof k, "pk", v, sizeof v);
    check(!strcmp(v, "abc123"), "raop pk=%s", v);

    check(dnssd_register_airplay(d, 7000) == 0 && f.registers == 2, "register_airplay");
    check(!strcmp(f.type, CADENCE_DNSSD_AIRPLAY) && !strcmp(f.name, "cadence-test"), "airplay advertised as %s %s",
          f.type, f.name);
    keys(f.txt, f.txt_len, k, sizeof k, "deviceid", v, sizeof v);
    check(!strcmp(k, "deviceid features pw flags model pk pi srcvers vv"), "airplay keys: %s", k);
    check(!strcmp(v, "02:11:22:33:44:A5"), "deviceid=%s", v);

    dnssd_unregister_airplay(d);
    dnssd_unregister_airplay(d);
    check(f.unregisters == 1 && !strcmp(f.type, CADENCE_DNSSD_AIRPLAY), "unregister_airplay: %d", f.unregisters);
    dnssd_destroy(d); /* withdraws raop, still registered */
    check(f.unregisters == 2 && !strcmp(f.type, CADENCE_DNSSD_RAOP), "destroy did not withdraw raop: %d", f.unregisters);

    /* the walker refuses what it cannot walk */
    const unsigned char bad[] = {5, 'a', '=', 'b'};
    const unsigned char nokey[] = {2, '=', 'x'};
    const unsigned char flag[] = {4, 'f', 'l', 'a', 'g'};
    size_t off = 0;
    const char *val;
    size_t vl;
    check(cadence_dnssd_txt_next(bad, sizeof bad, &off, k, sizeof k, &val, &vl) == -1, "a truncated entry walked");
    off = 0;
    check(cadence_dnssd_txt_next(nokey, sizeof nokey, &off, k, sizeof k, &val, &vl) == -1, "an empty key walked");
    off = 0;
    check(cadence_dnssd_txt_next(flag, sizeof flag, &off, k, 4, &val, &vl) == -1, "a key longer than its buffer");
    off = 0;
    check(cadence_dnssd_txt_next(flag, sizeof flag, &off, k, sizeof k, &val, &vl) == 1 && !val && !strcmp(k, "flag"),
          "a key without a value");
}

/* every callback set: raop calls some unchecked (a NULL audio_set_client_volume crashes GET /info) */
static void cb_audio(void *c, raop_ntp_t *n, audio_decode_struct *d) { (void) c; (void) n; (void) d; }
static void cb_video(void *c, raop_ntp_t *n, video_decode_struct *d) { (void) c; (void) n; (void) d; }
static void cb_void(void *c) { (void) c; }
static void cb_reset(void *c, int r) { (void) c; (void) r; }
static void cb_volume(void *c, float v) { (void) c; (void) v; }
static double cb_client_volume(void *c) { (void) c; return 0.0; }
static void cb_format(void *c, unsigned char *ct, unsigned short *spf, bool *s, bool *m, uint64_t *f) {
    (void) c; (void) ct; (void) spf; (void) s; (void) m; (void) f;
}
static void cb_video_reset(void *c, reset_type_t t) { (void) c; (void) t; }
static int cb_codec(void *c, video_codec_t k) { (void) c; (void) k; return 0; }
static void cb_admit(void *c, char *id, char *model, char *name, bool *admit) {
    (void) c; (void) id; (void) model; (void) name; *admit = true;
}
static void cb_log(void *c, int level, const char *msg) {
    (void) c;
    if (level <= LOGGER_ERR) {
        fprintf(stderr, "libairplay: %s\n", msg);
    }
}

/* one request on a fresh connection; the answer's status line and its body (content-length bytes) */
static int rtsp(unsigned short port, const char *req, char *status, size_t status_len, char **body, size_t *body_len) {
    int s = socket(AF_INET, SOCK_STREAM, 0);
    struct sockaddr_in a = {0};
    a.sin_family = AF_INET;
    a.sin_port = htons(port);
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    struct timeval tv = {5, 0};
    setsockopt(s, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
    if (s < 0 || connect(s, (struct sockaddr *) &a, sizeof a) < 0 || write(s, req, strlen(req)) != (ssize_t) strlen(req)) {
        if (s >= 0) {
            close(s);
        }
        return -1;
    }
    size_t cap = 65536, n = 0;
    char *buf = malloc(cap + 1);
    char *head_end = NULL;
    long want = -1;
    for (;;) {
        ssize_t r = read(s, buf + n, cap - n);
        if (r <= 0) {
            break;
        }
        n += (size_t) r;
        buf[n] = '\0';
        if (!head_end && (head_end = strstr(buf, "\r\n\r\n"))) {
            const char *cl = strcasestr(buf, "\r\nContent-Length:");
            want = cl && cl < head_end ? strtol(cl + 17, NULL, 10) : 0;
        }
        if (head_end && n >= (size_t) (head_end - buf) + 4 + (size_t) want) {
            break;
        }
        if (n == cap) {
            break;
        }
    }
    close(s);
    if (!head_end) {
        free(buf);
        return -1;
    }
    size_t line = (size_t) (strstr(buf, "\r\n") - buf);
    snprintf(status, status_len, "%.*s", (int) line, buf);
    size_t start = (size_t) (head_end - buf) + 4;
    *body_len = n - start;
    *body = malloc(*body_len + 1);
    memcpy(*body, buf + start, *body_len);
    free(buf);
    return 0;
}

static void test_receiver(void) {
    int err = 0;
    dnssd_t *d = dnssd_init("cadence-test", 12, hw, sizeof hw, &err, 0);
    raop_callbacks_t cbs;
    memset(&cbs, 0, sizeof cbs);
    cbs.audio_process = cb_audio;
    cbs.video_process = cb_video;
    cbs.video_pause = cb_void;
    cbs.video_resume = cb_void;
    cbs.conn_feedback = cb_void;
    cbs.conn_reset = cb_reset;
    cbs.video_reset = cb_video_reset;
    cbs.conn_init = cb_void;
    cbs.conn_destroy = cb_void;
    cbs.audio_flush = cb_void;
    cbs.video_flush = cb_void;
    cbs.audio_set_client_volume = cb_client_volume;
    cbs.audio_set_volume = cb_volume;
    cbs.audio_get_format = cb_format;
    cbs.report_client_request = cb_admit;
    cbs.video_set_codec = cb_codec;
    raop_t *raop = raop_init(&cbs);
    check(raop != NULL, "raop_init");
    if (!raop) {
        return;
    }
    raop_set_log_callback(raop, cb_log, NULL);
    raop_set_log_level(raop, LOGGER_ERR);
    char keyfile[] = "/tmp/airplay-test-XXXXXX";
    int fd = mkstemp(keyfile);
    close(fd);
    unlink(keyfile); /* raop_init2 writes a new key there */
    check(raop_init2(raop, 1, "02:11:22:33:44:A5", keyfile) == 0, "raop_init2");
    unsigned short port = 0;
    raop_set_port(raop, port);
    check(raop_start_httpd(raop, &port) >= 0 && port != 0, "raop_start_httpd: port %u", port);
    raop_set_dnssd(raop, d);
    dnssd_register_raop(d, port);
    dnssd_register_airplay(d, port);

    char status[128], *body = NULL;
    size_t body_len = 0;
    check(rtsp(port, "GET /info RTSP/1.0\r\nCSeq: 1\r\nUser-Agent: AirPlay/690.7.1\r\n\r\n", status, sizeof status, &body,
               &body_len) == 0,
          "GET /info: no answer");
    check(!strcmp(status, "RTSP/1.0 200 OK"), "GET /info: %s", status);
    plist_t info = NULL;
    if (body) {
        plist_from_bin(body, (uint32_t) body_len, &info);
    }
    check(info && plist_get_node_type(info) == PLIST_DICT, "GET /info: not a binary plist dict (%zu bytes)", body_len);
    if (info) {
        uint32_t n = plist_dict_get_size(info);
        const char *want_keys[] = {"deviceID", "macAddress", "pk", "features", "name", "model", "sourceVersion",
                                   "statusFlags", "audioLatencies"};
        for (size_t i = 0; i < sizeof want_keys / sizeof *want_keys; i++) {
            check(plist_dict_get_item(info, want_keys[i]) != NULL, "GET /info: no %s", want_keys[i]);
        }
        const char *strings[][2] = {{"name", "cadence-test"}, {"deviceID", "02:11:22:33:44:A5"}};
        for (size_t i = 0; i < 2; i++) {
            plist_t node = plist_dict_get_item(info, strings[i][0]);
            char *s = NULL;
            if (node && plist_get_node_type(node) == PLIST_STRING) {
                plist_get_string_val(node, &s);
            }
            check(s && !strcasecmp(s, strings[i][1]), "GET /info %s: %s", strings[i][0], s ? s : "(none)");
            free(s);
        }
        printf("GET /info: 200, %u keys\n", n);
        plist_free(info);
    }
    free(body);

    /* the BLE discovery query: the txt records alone, as data */
    body = NULL;
    check(rtsp(port, "GET /info?txtAirPlay&txtRAOP RTSP/1.0\r\n\r\n", status, sizeof status, &body, &body_len) == 0,
          "GET /info?txt: no answer");
    check(!strcmp(status, "RTSP/1.0 200 OK"), "GET /info?txt: %s", status);
    info = NULL;
    if (body) {
        plist_from_bin(body, (uint32_t) body_len, &info);
    }
    plist_t ap = info ? plist_dict_get_item(info, "txtAirPlay") : NULL;
    char *data = NULL;
    uint64_t data_len = 0;
    if (ap) {
        plist_get_data_val(ap, &data, &data_len);
    }
    int len = 0;
    const char *want = dnssd_get_airplay_txt(d, &len);
    check(data && data_len == (uint64_t) len && !memcmp(data, want, (size_t) len), "txtAirPlay differs from the records");
    free(data);
    if (info) {
        plist_free(info);
    }
    free(body);

    raop_stop_httpd(raop);
    raop_destroy(raop);
    dnssd_destroy(d);
    unlink(keyfile);
}

#ifdef CADENCE_AIRPLAY_TEST_AVAHI
static void test_avahi(void) {
    int err = 0;
    /* libairplay.sh runs the test with a private bus as the system bus, which has no avahi-daemon on it */
    cadence_dnssd_backend_t *b = cadence_dnssd_avahi_new(&err);
    check(b != NULL, "cadence_dnssd_avahi_new: %d", err);
    if (!b) {
        return;
    }
    dnssd_t *d = dnssd_init("cadence-test", 12, hw, sizeof hw, &err, 0);
    cadence_dnssd_set_backend(d, b);
    /* no daemon: queued until one runs */
    check(dnssd_register_raop(d, 7000) == 0, "avahi register_raop");
    check(dnssd_register_raop(d, 7001) == 0, "avahi register_raop again");
    check(dnssd_register_airplay(d, 7000) == 0, "avahi register_airplay");
    dnssd_destroy(d);
    cadence_dnssd_avahi_free(b);
}
#endif

int main(int argc, char **argv) {
    test_records();
    if (argc < 2 || strcmp(argv[1], "--records-only") != 0) {
        test_receiver();
    }
#ifdef CADENCE_AIRPLAY_TEST_AVAHI
    test_avahi();
#endif
    if (failures) {
        fprintf(stderr, "%d failures\n", failures);
        return 1;
    }
    printf("ok\n");
    return 0;
}

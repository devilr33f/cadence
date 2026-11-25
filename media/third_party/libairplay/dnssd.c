/* uxplay's dnssd.h api over cadence_dnssd.h's backends (see there). the records are built with the same calls in
 * the same order as uxplay's dnssd.c, with bonjour's TXTRecordSetValue semantics (a key set again is removed and
 * appended), so a receiver advertises what uxplay would */
#include "cadence_dnssd.h"

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "dnssdint.h"

#define TXT_MAX 1300 /* one mdns packet's worth */

typedef struct txt {
    unsigned char buf[TXT_MAX];
    size_t len;
} txt_t;

struct dnssd_s {
    char *name;
    int name_len;
    char *hw_addr;
    int hw_addr_len;
    char *pk;
    uint32_t features1;
    uint32_t features2;
    unsigned char pin_pw;
    txt_t raop;
    txt_t airplay;
    cadence_dnssd_backend_t backend;
    int has_backend;
    int raop_registered;
    int airplay_registered;
};

static void txt_remove(txt_t *t, const char *key) {
    size_t kl = strlen(key), off = 0;
    while (off < t->len) {
        size_t n = t->buf[off];
        const unsigned char *e = t->buf + off + 1;
        if (n >= kl && memcmp(e, key, kl) == 0 && (n == kl || e[kl] == '=')) {
            memmove(t->buf + off, t->buf + off + 1 + n, t->len - off - 1 - n);
            t->len -= 1 + n;
            return;
        }
        off += 1 + n;
    }
}

static void txt_set(txt_t *t, const char *key, const char *val) {
    size_t kl = strlen(key), vl = strlen(val), n = kl + 1 + vl;
    txt_remove(t, key);
    if (n > 255 || t->len + 1 + n > TXT_MAX) {
        return;
    }
    t->buf[t->len++] = (unsigned char) n;
    memcpy(t->buf + t->len, key, kl);
    t->len += kl;
    t->buf[t->len++] = '=';
    memcpy(t->buf + t->len, val, vl);
    t->len += vl;
}

static void features_string(const dnssd_t *d, char out[22]) {
    snprintf(out, 22, "0x%X,0x%X", (unsigned) d->features1, (unsigned) d->features2);
}

static int parse_features(const char *s, uint32_t *out) {
    char *end = NULL;
    unsigned long long v = strtoull(s, &end, 16);
    if (!end || *end || v > 0xFFFFFFFFull) {
        return -1;
    }
    *out = (uint32_t) v;
    return 0;
}

dnssd_t *dnssd_init(const char *name, int name_len, const char *hw_addr, int hw_addr_len, int *error,
                    unsigned char pin_pw) {
    if (error) {
        *error = DNSSD_ERROR_NOERROR;
    }
    if (hw_addr_len <= 0 || hw_addr_len > MAX_HWADDR_LEN) {
        if (error) {
            *error = DNSSD_ERROR_HWADDRLEN;
        }
        return NULL;
    }
    dnssd_t *d = calloc(1, sizeof(*d));
    if (!d) {
        goto oom;
    }
    if (parse_features(FEATURES_1, &d->features1) || parse_features(FEATURES_2, &d->features2)) {
        free(d);
        if (error) {
            *error = DNSSD_ERROR_BADFEATURES;
        }
        return NULL;
    }
    d->name = calloc(1, (size_t) name_len + 1);
    d->hw_addr = malloc((size_t) hw_addr_len);
    d->pk = strdup("");
    if (!d->name || !d->hw_addr || !d->pk) {
        dnssd_destroy(d);
        goto oom;
    }
    memcpy(d->name, name, (size_t) name_len);
    d->name_len = name_len;
    memcpy(d->hw_addr, hw_addr, (size_t) hw_addr_len);
    d->hw_addr_len = hw_addr_len;
    d->pin_pw = pin_pw;
    return d;
oom:
    if (error) {
        *error = DNSSD_ERROR_OUTOFMEM;
    }
    return NULL;
}

void cadence_dnssd_set_backend(dnssd_t *d, const cadence_dnssd_backend_t *backend) {
    if (backend) {
        d->backend = *backend;
        d->has_backend = 1;
    } else {
        memset(&d->backend, 0, sizeof(d->backend));
        d->has_backend = 0;
    }
}

static int advertise(dnssd_t *d, const char *type, const char *name, unsigned short port, const txt_t *t) {
    if (!d->has_backend || !d->backend.register_service) {
        return 0;
    }
    return d->backend.register_service(d->backend.ctx, type, name, port, t->buf, t->len);
}

int dnssd_register_raop(dnssd_t *d, unsigned short port) {
    char features[22];
    txt_t *t = &d->raop;
    features_string(d, features);
    t->len = 0;
    txt_set(t, "ch", RAOP_CH);
    txt_set(t, "cn", RAOP_CN);
    txt_set(t, "da", RAOP_DA);
    txt_set(t, "et", RAOP_ET);
    txt_set(t, "vv", RAOP_VV);
    txt_set(t, "ft", features);
    txt_set(t, "am", GLOBAL_MODEL);
    txt_set(t, "md", RAOP_MD);
    txt_set(t, "rhd", RAOP_RHD);
    switch (d->pin_pw) {
    case 2:
    case 3:
        txt_set(t, "pw", "true");
        txt_set(t, "sf", "0x84");
        break;
    case 1:
        txt_set(t, "pw", "true");
        txt_set(t, "sf", "0x8c");
        break;
    default:
        txt_set(t, "pw", "false");
        txt_set(t, "sf", RAOP_SF);
        break;
    }
    txt_set(t, "sr", RAOP_SR);
    txt_set(t, "ss", RAOP_SS);
    txt_set(t, "sv", RAOP_SV);
    txt_set(t, "tp", RAOP_TP);
    txt_set(t, "txtvers", RAOP_TXTVERS);
    txt_set(t, "sf", RAOP_SF);
    txt_set(t, "vs", RAOP_VS);
    txt_set(t, "vn", RAOP_VN);
    txt_set(t, "pk", d->pk);

    /* "<hw address in hex>@<name>", as uxplay's utils_hwaddr_raop */
    char servname[2 * MAX_HWADDR_LEN + 1 + 256];
    if ((size_t) d->name_len > sizeof(servname) - 2 * MAX_HWADDR_LEN - 2) {
        return -2;
    }
    int j = 0;
    for (int i = 0; i < d->hw_addr_len; i++) {
        j += snprintf(servname + j, sizeof(servname) - (size_t) j, "%02X", (unsigned char) d->hw_addr[i]);
    }
    snprintf(servname + j, sizeof(servname) - (size_t) j, "@%s", d->name);
    int rc = advertise(d, CADENCE_DNSSD_RAOP, servname, port, t);
    d->raop_registered = rc == 0;
    return rc;
}

int dnssd_register_airplay(dnssd_t *d, unsigned short port) {
    char features[22];
    char device_id[3 * MAX_HWADDR_LEN];
    txt_t *t = &d->airplay;
    features_string(d, features);
    int j = 0;
    for (int i = 0; i < d->hw_addr_len; i++) {
        j += snprintf(device_id + j, sizeof(device_id) - (size_t) j, i ? ":%02X" : "%02X", (unsigned char) d->hw_addr[i]);
    }
    t->len = 0;
    txt_set(t, "deviceid", device_id);
    txt_set(t, "features", features);
    txt_set(t, "pw", d->pin_pw ? "true" : "false");
    txt_set(t, "flags", "0x4");
    txt_set(t, "model", GLOBAL_MODEL);
    txt_set(t, "pk", d->pk);
    txt_set(t, "pi", AIRPLAY_PI);
    txt_set(t, "srcvers", AIRPLAY_SRCVERS);
    txt_set(t, "vv", AIRPLAY_VV);
    int rc = advertise(d, CADENCE_DNSSD_AIRPLAY, d->name, port, t);
    d->airplay_registered = rc == 0;
    return rc;
}

static void withdraw(dnssd_t *d, const char *type, int *registered) {
    if (*registered && d->has_backend && d->backend.unregister_service) {
        d->backend.unregister_service(d->backend.ctx, type);
    }
    *registered = 0;
}

void dnssd_unregister_raop(dnssd_t *d) {
    withdraw(d, CADENCE_DNSSD_RAOP, &d->raop_registered);
}

void dnssd_unregister_airplay(dnssd_t *d) {
    withdraw(d, CADENCE_DNSSD_AIRPLAY, &d->airplay_registered);
}

const char *dnssd_get_raop_txt(dnssd_t *d, int *length) {
    *length = (int) d->raop.len;
    return (const char *) d->raop.buf;
}

const char *dnssd_get_airplay_txt(dnssd_t *d, int *length) {
    *length = (int) d->airplay.len;
    return (const char *) d->airplay.buf;
}

const char *dnssd_get_name(dnssd_t *d, int *length) {
    *length = d->name_len;
    return d->name;
}

const char *dnssd_get_hw_addr(dnssd_t *d, int *length) {
    *length = d->hw_addr_len;
    return d->hw_addr;
}

void dnssd_set_airplay_features(dnssd_t *d, int bit, int val) {
    if (bit < 0 || bit > 63 || val < 0 || val > 1) {
        return;
    }
    uint32_t *f = bit < 32 ? &d->features1 : &d->features2;
    uint32_t mask = 1u << (bit % 32);
    *f = val ? (*f | mask) : (*f & ~mask);
}

uint64_t dnssd_get_airplay_features(dnssd_t *d) {
    return ((uint64_t) d->features2 << 32) | d->features1;
}

void dnssd_set_pk(dnssd_t *d, char *pk_str) {
    char *pk = strdup(pk_str ? pk_str : "");
    if (pk) {
        free(d->pk);
        d->pk = pk;
    }
}

void dnssd_destroy(dnssd_t *d) {
    if (!d) {
        return;
    }
    dnssd_unregister_raop(d);
    dnssd_unregister_airplay(d);
    free(d->name);
    free(d->hw_addr);
    free(d->pk);
    free(d);
}

int cadence_dnssd_txt_next(const unsigned char *txt, size_t txt_len, size_t *offset, char *key_buf, size_t key_buf_len,
                           const char **value, size_t *value_len) {
    size_t off = *offset;
    if (off >= txt_len) {
        return 0;
    }
    size_t n = txt[off];
    if (n == 0 || off + 1 + n > txt_len) {
        return -1;
    }
    const char *e = (const char *) txt + off + 1;
    const char *eq = memchr(e, '=', n);
    size_t kl = eq ? (size_t) (eq - e) : n;
    if (kl == 0 || kl >= key_buf_len) {
        return -1;
    }
    memcpy(key_buf, e, kl);
    key_buf[kl] = '\0';
    *value = eq ? eq + 1 : NULL;
    *value_len = eq ? n - kl - 1 : 0;
    *offset = off + 1 + n;
    return 1;
}

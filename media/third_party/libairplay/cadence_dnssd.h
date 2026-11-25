/* cadence's dnssd for libairplay: uxplay's dnssd.h api (which raop.c calls for its txt records), with the mdns
 * advertising handed to a backend. uxplay's own dnssd.c talks to bonjour or avahi's dns_sd compat library; this one
 * keeps the records and calls whichever backend the embedder sets:
 *   - linux: cadence_dnssd_avahi_new() (avahi-client, its own threaded poll), from libairplay_dnssd_avahi;
 *   - android: a backend the app fills in from jni, whose calls go to NsdManager (stream F's hook point);
 *   - none set: nothing is advertised, the records are still built (the tests, and GET /info's txt queries).
 * every backend call is made on the thread that called dnssd_register_* or dnssd_unregister_* */
#ifndef CADENCE_DNSSD_H
#define CADENCE_DNSSD_H

#include <stddef.h>
#include <stdint.h>

#include "dnssd.h"

#ifdef __cplusplus
extern "C" {
#endif

#define CADENCE_DNSSD_RAOP "_raop._tcp"
#define CADENCE_DNSSD_AIRPLAY "_airplay._tcp"

typedef struct cadence_dnssd_backend {
    void *ctx;
    /* advertise (or re-advertise with new records) one service: type is one of the two above, name its instance
     * name ("<hwaddr hex>@<name>" for raop, "<name>" for airplay), txt its records in rfc 6763 wire format (each
     * entry a length byte and key=value; cadence_dnssd_txt_next walks them). returns 0, or a negative errno */
    int (*register_service)(void *ctx, const char *type, const char *name, uint16_t port, const unsigned char *txt,
                            size_t txt_len);
    /* withdraw one service; a type never registered is ignored */
    void (*unregister_service)(void *ctx, const char *type);
} cadence_dnssd_backend_t;

/* sets the backend of one dnssd (after dnssd_init, before raop_set_dnssd); the struct is copied, its ctx is the
 * caller's to keep alive until dnssd_destroy. NULL clears it: nothing is advertised */
void cadence_dnssd_set_backend(dnssd_t *dnssd, const cadence_dnssd_backend_t *backend);

/* walks a txt record set: from *offset, sets key (nul-terminated, into key_buf), *value and *value_len (not
 * terminated) and advances *offset; returns 1 for an entry, 0 at the end, -1 for a malformed set or a key longer
 * than key_buf. an entry without '=' has a NULL value */
int cadence_dnssd_txt_next(const unsigned char *txt, size_t txt_len, size_t *offset, char *key_buf, size_t key_buf_len,
                           const char **value, size_t *value_len);

/* the avahi-client backend (linux, libairplay_dnssd_avahi): connects to the system's avahi-daemon on a poll thread
 * of its own and keeps the services registered across daemon restarts and name collisions. NULL on failure, with
 * *error set to avahi's error code */
cadence_dnssd_backend_t *cadence_dnssd_avahi_new(int *error);
void cadence_dnssd_avahi_free(cadence_dnssd_backend_t *backend);

#ifdef __cplusplus
}
#endif

#endif

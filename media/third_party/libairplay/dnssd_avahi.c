/* cadence_dnssd.h's avahi-client backend: one entry group per service type on a threaded poll of its own. the
 * client is created with AVAHI_CLIENT_NO_FAIL, so a missing or restarting avahi-daemon is waited for and every
 * service is added again once it runs; a name collision takes avahi's alternative name */
#include <avahi-client/client.h>
#include <avahi-client/publish.h>
#include <avahi-common/alternative.h>
#include <avahi-common/error.h>
#include <avahi-common/malloc.h>
#include <avahi-common/strlst.h>
#include <avahi-common/thread-watch.h>
#include <errno.h>
#include <stdlib.h>
#include <string.h>

#include "cadence_dnssd.h"

#define SERVICES 2

typedef struct service {
    const char *type; /* NULL while unused */
    char *name;
    uint16_t port;
    unsigned char *txt;
    size_t txt_len;
    AvahiEntryGroup *group;
} service_t;

typedef struct avahi_backend {
    cadence_dnssd_backend_t ops; /* first: the pointer handed out is this struct's */
    AvahiThreadedPoll *poll;
    AvahiClient *client;
    service_t services[SERVICES];
} avahi_backend_t;

static void group_cb(AvahiEntryGroup *g, AvahiEntryGroupState state, void *userdata);

static AvahiStringList *txt_list(const service_t *s) {
    AvahiStringList *l = NULL;
    size_t off = 0;
    while (off < s->txt_len) {
        size_t n = s->txt[off];
        if (off + 1 + n > s->txt_len) {
            break;
        }
        l = avahi_string_list_add_arbitrary(l, s->txt + off + 1, n);
        off += 1 + n;
    }
    /* avahi prepends, so the list is reversed: back to the records' order */
    return avahi_string_list_reverse(l);
}

/* with the poll locked, or on the poll thread */
static int publish(avahi_backend_t *b, service_t *s) {
    if (!b->client || avahi_client_get_state(b->client) != AVAHI_CLIENT_S_RUNNING) {
        return 0; /* added once the client runs */
    }
    if (!s->group) {
        s->group = avahi_entry_group_new(b->client, group_cb, b);
        if (!s->group) {
            return -EIO;
        }
    } else {
        avahi_entry_group_reset(s->group);
    }
    AvahiStringList *l = txt_list(s);
    int rc = avahi_entry_group_add_service_strlst(s->group, AVAHI_IF_UNSPEC, AVAHI_PROTO_UNSPEC, 0, s->name, s->type,
                                                  NULL, NULL, s->port, l);
    avahi_string_list_free(l);
    if (rc == AVAHI_ERR_COLLISION) {
        char *alt = avahi_alternative_service_name(s->name);
        avahi_free(s->name);
        s->name = alt;
        return publish(b, s);
    }
    if (rc < 0 || (rc = avahi_entry_group_commit(s->group)) < 0) {
        return -EIO;
    }
    return 0;
}

static void group_cb(AvahiEntryGroup *g, AvahiEntryGroupState state, void *userdata) {
    avahi_backend_t *b = userdata;
    if (state != AVAHI_ENTRY_GROUP_COLLISION) {
        return;
    }
    for (int i = 0; i < SERVICES; i++) {
        service_t *s = &b->services[i];
        if (s->type && s->group == g) {
            char *alt = avahi_alternative_service_name(s->name);
            avahi_free(s->name);
            s->name = alt;
            publish(b, s);
        }
    }
}

static void client_cb(AvahiClient *c, AvahiClientState state, void *userdata) {
    avahi_backend_t *b = userdata;
    b->client = c; /* the callback can run inside avahi_client_new, before it returns */
    switch (state) {
    case AVAHI_CLIENT_S_RUNNING:
        for (int i = 0; i < SERVICES; i++) {
            if (b->services[i].type) {
                publish(b, &b->services[i]);
            }
        }
        break;
    case AVAHI_CLIENT_S_COLLISION:
    case AVAHI_CLIENT_S_REGISTERING:
        for (int i = 0; i < SERVICES; i++) {
            if (b->services[i].group) {
                avahi_entry_group_reset(b->services[i].group);
            }
        }
        break;
    case AVAHI_CLIENT_FAILURE:
        /* the daemon went away: a new client waits for it (NO_FAIL), and its S_RUNNING adds everything again */
        for (int i = 0; i < SERVICES; i++) {
            b->services[i].group = NULL; /* freed with the client */
        }
        avahi_client_free(c);
        b->client = NULL;
        {
            int err = 0;
            avahi_client_new(avahi_threaded_poll_get(b->poll), AVAHI_CLIENT_NO_FAIL, client_cb, b, &err);
        }
        break;
    case AVAHI_CLIENT_CONNECTING:
        break;
    }
}

static service_t *find(avahi_backend_t *b, const char *type, int add) {
    for (int i = 0; i < SERVICES; i++) {
        if (b->services[i].type && strcmp(b->services[i].type, type) == 0) {
            return &b->services[i];
        }
    }
    if (!add) {
        return NULL;
    }
    for (int i = 0; i < SERVICES; i++) {
        if (!b->services[i].type) {
            return &b->services[i];
        }
    }
    return NULL;
}

static void clear(service_t *s) {
    if (s->group) {
        avahi_entry_group_free(s->group);
    }
    avahi_free(s->name);
    free(s->txt);
    memset(s, 0, sizeof(*s));
}

static int register_service(void *ctx, const char *type, const char *name, uint16_t port, const unsigned char *txt,
                            size_t txt_len) {
    avahi_backend_t *b = ctx;
    const char *known = strcmp(type, CADENCE_DNSSD_RAOP) == 0      ? CADENCE_DNSSD_RAOP
                        : strcmp(type, CADENCE_DNSSD_AIRPLAY) == 0 ? CADENCE_DNSSD_AIRPLAY
                                                                   : NULL;
    if (!known) {
        return -EINVAL;
    }
    unsigned char *copy = malloc(txt_len ? txt_len : 1);
    char *n = avahi_strdup(name);
    if (!copy || !n) {
        free(copy);
        avahi_free(n);
        return -ENOMEM;
    }
    memcpy(copy, txt, txt_len);
    avahi_threaded_poll_lock(b->poll);
    service_t *s = find(b, known, 1);
    AvahiEntryGroup *group = s->group;
    s->group = NULL;
    clear(s);
    s->group = group;
    s->type = known;
    s->name = n;
    s->port = port;
    s->txt = copy;
    s->txt_len = txt_len;
    int rc = publish(b, s);
    avahi_threaded_poll_unlock(b->poll);
    return rc;
}

static void unregister_service(void *ctx, const char *type) {
    avahi_backend_t *b = ctx;
    avahi_threaded_poll_lock(b->poll);
    service_t *s = find(b, type, 0);
    if (s) {
        clear(s);
    }
    avahi_threaded_poll_unlock(b->poll);
}

cadence_dnssd_backend_t *cadence_dnssd_avahi_new(int *error) {
    int err = 0;
    avahi_backend_t *b = calloc(1, sizeof(*b));
    if (!b) {
        err = AVAHI_ERR_NO_MEMORY;
        goto fail;
    }
    b->ops.ctx = b;
    b->ops.register_service = register_service;
    b->ops.unregister_service = unregister_service;
    b->poll = avahi_threaded_poll_new();
    if (!b->poll) {
        err = AVAHI_ERR_NO_MEMORY;
        goto fail;
    }
    if (!avahi_client_new(avahi_threaded_poll_get(b->poll), AVAHI_CLIENT_NO_FAIL, client_cb, b, &err)) {
        goto fail;
    }
    if (avahi_threaded_poll_start(b->poll) < 0) {
        err = AVAHI_ERR_FAILURE;
        goto fail;
    }
    return &b->ops;
fail:
    if (error) {
        *error = err;
    }
    if (b) {
        if (b->client) {
            avahi_client_free(b->client);
        }
        if (b->poll) {
            avahi_threaded_poll_free(b->poll);
        }
        free(b);
    }
    return NULL;
}

void cadence_dnssd_avahi_free(cadence_dnssd_backend_t *ops) {
    if (!ops) {
        return;
    }
    avahi_backend_t *b = (avahi_backend_t *) ops;
    avahi_threaded_poll_stop(b->poll);
    for (int i = 0; i < SERVICES; i++) {
        clear(&b->services[i]);
    }
    if (b->client) {
        avahi_client_free(b->client);
    }
    avahi_threaded_poll_free(b->poll);
    free(b);
}

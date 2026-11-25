/* cadence pcm ring: mpv's ao_cadence (writer) -> audiod's mixer (reader), shared by c and c++. this is the canonical
 * copy: media/third_party/mpv's ao_cadence patch carries the same header, and a change here bumps the version.
 *
 * one memfd: this header, then `capacity` frames of interleaved f32 stereo 48 kHz. one eventfd: the reader adds 1
 * after every period it consumes, so the writer wakes when there is room (the writer also uses it to wake itself).
 * positions are frame counters that never wrap in practice (u64); slot = pos & (capacity - 1).
 *
 * flush (mpv's reset on seek): the writer stores flush_pos = write_pos, then bumps generation (release). a reader
 * that sees a new generation moves read_pos to max(read_pos, flush_pos), so audio queued before the flush is never
 * handed out after it (at most the period being read while the flush happened).
 *
 * latency: after each period the reader publishes, under a seqlock, the pair (read_pos, heard_ns): the
 * CLOCK_MONOTONIC time at which the frame at read_pos will be heard (its period + the mixer's limiter + the
 * device's latency from AAudio getTimestamp or snd_pcm_delay). the writer then knows when any frame it queues will
 * be heard: heard_ns + (pos - read_pos) / rate. latency_ns = heard_ns - stamp_ns is kept for logs. */
#pragma once
#include <stdint.h>

#ifdef __cplusplus
static_assert(__atomic_always_lock_free(8, 0), "the ring's u64 positions cross processes: they must be lock-free");
#else
_Static_assert(__atomic_always_lock_free(8, 0), "the ring's u64 positions cross processes: they must be lock-free");
#endif

#define CADENCE_RING_MAGIC 0x47524443u /* "CDRG" */
#define CADENCE_RING_VERSION 1u
#define CADENCE_RING_RATE 48000u
#define CADENCE_RING_CHANNELS 2u

enum { CADENCE_RING_STOPPED = 0, CADENCE_RING_STREAMING = 1, CADENCE_RING_PAUSED = 2 };

struct cadence_ring_header {
    /* set once by whoever creates the memfd, before either side runs */
    uint32_t magic, version, rate, channels;
    uint32_t capacity;    /* frames, a power of two */
    uint32_t data_offset; /* bytes from the start of the header to frame 0 */
    uint8_t pad0[40];

    /* the writer's line */
    uint64_t write_pos;    /* frames ever committed */
    uint64_t flush_pos;    /* write_pos at the last flush */
    uint32_t generation;   /* bumped (release) after flush_pos is stored */
    uint32_t writer_state; /* CADENCE_RING_*: the reader may log it, never acts on it */
    uint8_t pad1[40];

    /* the reader's line */
    uint64_t read_pos;        /* frames ever consumed or skipped by a flush */
    uint32_t seen_generation; /* the reader's own */
    uint32_t seq;             /* seqlock over read_pos_stamped, heard_ns, stamp_ns, latency_ns */
    uint64_t read_pos_stamped;
    int64_t heard_ns;   /* CLOCK_MONOTONIC: when the frame at read_pos_stamped is heard */
    int64_t stamp_ns;   /* CLOCK_MONOTONIC: when the reader published it */
    int64_t latency_ns; /* heard_ns - stamp_ns */
    uint32_t underruns; /* reads that found less than a period while the writer streamed */
    uint8_t pad2[12];
};

#define CADENCE_RING_DATA_OFFSET 256u
#define CADENCE_RING_LOAD(p) __atomic_load_n((p), __ATOMIC_ACQUIRE)
#define CADENCE_RING_STORE(p, v) __atomic_store_n((p), (v), __ATOMIC_RELEASE)
#define CADENCE_RING_RELAXED(p) __atomic_load_n((p), __ATOMIC_RELAXED)

static inline uint64_t cadence_ring_bytes(uint32_t capacity)
{
    return CADENCE_RING_DATA_OFFSET + (uint64_t)capacity * CADENCE_RING_CHANNELS * sizeof(float);
}

static inline float *cadence_ring_data(struct cadence_ring_header *h)
{
    return (float *)((uint8_t *)h + h->data_offset);
}

/* the writer's consistent read of the reader's timestamp; false until the reader published one */
static inline int cadence_ring_timestamp(struct cadence_ring_header *h, uint64_t *pos, int64_t *heard_ns)
{
    for (int tries = 0; tries < 64; tries++) {
        uint32_t s0 = CADENCE_RING_LOAD(&h->seq);
        if (s0 & 1)
            continue;
        uint64_t p = CADENCE_RING_RELAXED(&h->read_pos_stamped);
        int64_t t = CADENCE_RING_RELAXED(&h->heard_ns);
        __atomic_thread_fence(__ATOMIC_ACQUIRE);
        if (CADENCE_RING_RELAXED(&h->seq) == s0) {
            *pos = p;
            *heard_ns = t;
            return t != 0;
        }
    }
    return 0;
}

#ifdef __cplusplus
static_assert(sizeof(struct cadence_ring_header) == 192, "three 64-byte lines, before the data at 256");
#else
_Static_assert(sizeof(struct cadence_ring_header) == 192, "three 64-byte lines, before the data at 256");
#endif

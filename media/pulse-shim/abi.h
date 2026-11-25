#pragma once

// our own c definitions, written from the field and enumerator lists of the libpulse api documentation: the types,
// constants and the 48 functions soloist resolves (symbols.txt), nothing else

#include <sys/time.h>

#include <cstddef>
#include <cstdint>

#define CADENCE_PA_EXPORT __attribute__((visibility("default")))

// the types and constants are plain declarations, laid out as c lays them out; the functions have c linkage

typedef std::uint32_t pa_volume_t;
typedef std::uint64_t pa_usec_t;
typedef int pa_sample_format_t;
typedef int pa_channel_position_t;
typedef int pa_context_state_t;
typedef int pa_stream_state_t;
typedef int pa_operation_state_t;
typedef unsigned pa_context_flags_t;
typedef unsigned pa_subscription_mask_t;
typedef unsigned pa_subscription_event_type_t;
typedef unsigned pa_io_event_flags_t;
typedef unsigned pa_stream_flags_t;
typedef int pa_seek_mode_t;

inline constexpr std::size_t PA_CHANNELS_MAX = 32;
inline constexpr std::uint32_t PA_RATE_MAX = 48000 * 8;
inline constexpr std::uint32_t PA_INVALID_INDEX = 0xffffffffU;
inline constexpr pa_volume_t PA_VOLUME_MUTED = 0;
inline constexpr pa_volume_t PA_VOLUME_NORM = 0x10000;
inline constexpr pa_volume_t PA_VOLUME_MAX = 0xffffffffU / 2;

inline constexpr pa_sample_format_t PA_SAMPLE_U8 = 0;
inline constexpr pa_sample_format_t PA_SAMPLE_ALAW = 1;
inline constexpr pa_sample_format_t PA_SAMPLE_ULAW = 2;
inline constexpr pa_sample_format_t PA_SAMPLE_S16LE = 3;
inline constexpr pa_sample_format_t PA_SAMPLE_S16BE = 4;
inline constexpr pa_sample_format_t PA_SAMPLE_FLOAT32LE = 5;
inline constexpr pa_sample_format_t PA_SAMPLE_FLOAT32BE = 6;
inline constexpr pa_sample_format_t PA_SAMPLE_S32LE = 7;
inline constexpr pa_sample_format_t PA_SAMPLE_S32BE = 8;
inline constexpr pa_sample_format_t PA_SAMPLE_S24LE = 9;
inline constexpr pa_sample_format_t PA_SAMPLE_S24BE = 10;
inline constexpr pa_sample_format_t PA_SAMPLE_S24_32LE = 11;
inline constexpr pa_sample_format_t PA_SAMPLE_S24_32BE = 12;
inline constexpr pa_sample_format_t PA_SAMPLE_MAX = 13;
inline constexpr pa_sample_format_t PA_SAMPLE_INVALID = -1;

inline constexpr pa_channel_position_t PA_CHANNEL_POSITION_INVALID = -1;
inline constexpr pa_channel_position_t PA_CHANNEL_POSITION_MONO = 0;
inline constexpr pa_channel_position_t PA_CHANNEL_POSITION_FRONT_LEFT = 1;
inline constexpr pa_channel_position_t PA_CHANNEL_POSITION_FRONT_RIGHT = 2;

inline constexpr pa_context_state_t PA_CONTEXT_UNCONNECTED = 0;
inline constexpr pa_context_state_t PA_CONTEXT_CONNECTING = 1;
inline constexpr pa_context_state_t PA_CONTEXT_AUTHORIZING = 2;
inline constexpr pa_context_state_t PA_CONTEXT_SETTING_NAME = 3;
inline constexpr pa_context_state_t PA_CONTEXT_READY = 4;
inline constexpr pa_context_state_t PA_CONTEXT_FAILED = 5;
inline constexpr pa_context_state_t PA_CONTEXT_TERMINATED = 6;

inline constexpr pa_stream_state_t PA_STREAM_UNCONNECTED = 0;
inline constexpr pa_stream_state_t PA_STREAM_CREATING = 1;
inline constexpr pa_stream_state_t PA_STREAM_READY = 2;
inline constexpr pa_stream_state_t PA_STREAM_FAILED = 3;
inline constexpr pa_stream_state_t PA_STREAM_TERMINATED = 4;

inline constexpr pa_operation_state_t PA_OPERATION_RUNNING = 0;
inline constexpr pa_operation_state_t PA_OPERATION_DONE = 1;
inline constexpr pa_operation_state_t PA_OPERATION_CANCELLED = 2;

inline constexpr pa_context_flags_t PA_CONTEXT_NOFLAGS = 0;
inline constexpr pa_context_flags_t PA_CONTEXT_NOAUTOSPAWN = 1;
inline constexpr pa_context_flags_t PA_CONTEXT_NOFAIL = 2;

inline constexpr pa_stream_flags_t PA_STREAM_NOFLAGS = 0;
inline constexpr pa_stream_flags_t PA_STREAM_START_CORKED = 0x1;
inline constexpr pa_stream_flags_t PA_STREAM_AUTO_TIMING_UPDATE = 0x8;

inline constexpr pa_seek_mode_t PA_SEEK_RELATIVE = 0;

inline constexpr pa_subscription_mask_t PA_SUBSCRIPTION_MASK_NULL = 0;
inline constexpr pa_subscription_mask_t PA_SUBSCRIPTION_MASK_SINK_INPUT = 0x4;
inline constexpr pa_subscription_mask_t PA_SUBSCRIPTION_MASK_ALL = 0x2ff;

inline constexpr pa_subscription_event_type_t PA_SUBSCRIPTION_EVENT_SINK_INPUT = 0x2;
inline constexpr pa_subscription_event_type_t PA_SUBSCRIPTION_EVENT_FACILITY_MASK = 0xf;
inline constexpr pa_subscription_event_type_t PA_SUBSCRIPTION_EVENT_NEW = 0;
inline constexpr pa_subscription_event_type_t PA_SUBSCRIPTION_EVENT_CHANGE = 0x10;
inline constexpr pa_subscription_event_type_t PA_SUBSCRIPTION_EVENT_REMOVE = 0x20;
inline constexpr pa_subscription_event_type_t PA_SUBSCRIPTION_EVENT_TYPE_MASK = 0x30;

inline constexpr pa_io_event_flags_t PA_IO_EVENT_NULL = 0;
inline constexpr pa_io_event_flags_t PA_IO_EVENT_INPUT = 1;
inline constexpr pa_io_event_flags_t PA_IO_EVENT_OUTPUT = 2;
inline constexpr pa_io_event_flags_t PA_IO_EVENT_HANGUP = 4;
inline constexpr pa_io_event_flags_t PA_IO_EVENT_ERROR = 8;

inline constexpr int PA_OK = 0;
inline constexpr int PA_ERR_ACCESS = 1;
inline constexpr int PA_ERR_COMMAND = 2;
inline constexpr int PA_ERR_INVALID = 3;
inline constexpr int PA_ERR_EXIST = 4;
inline constexpr int PA_ERR_NOENTITY = 5;
inline constexpr int PA_ERR_CONNECTIONREFUSED = 6;
inline constexpr int PA_ERR_PROTOCOL = 7;
inline constexpr int PA_ERR_TIMEOUT = 8;
inline constexpr int PA_ERR_AUTHKEY = 9;
inline constexpr int PA_ERR_INTERNAL = 10;
inline constexpr int PA_ERR_CONNECTIONTERMINATED = 11;
inline constexpr int PA_ERR_KILLED = 12;
inline constexpr int PA_ERR_INVALIDSERVER = 13;
inline constexpr int PA_ERR_MODINITFAILED = 14;
inline constexpr int PA_ERR_BADSTATE = 15;
inline constexpr int PA_ERR_NODATA = 16;
inline constexpr int PA_ERR_VERSION = 17;
inline constexpr int PA_ERR_TOOLARGE = 18;
inline constexpr int PA_ERR_NOTSUPPORTED = 19;
inline constexpr int PA_ERR_UNKNOWN = 20;
inline constexpr int PA_ERR_NOEXTENSION = 21;
inline constexpr int PA_ERR_OBSOLETE = 22;
inline constexpr int PA_ERR_NOTIMPLEMENTED = 23;
inline constexpr int PA_ERR_FORKED = 24;
inline constexpr int PA_ERR_IO = 25;
inline constexpr int PA_ERR_BUSY = 26;
inline constexpr int PA_ERR_MAX = 27;

typedef struct pa_sample_spec {
  pa_sample_format_t format;
  std::uint32_t rate;
  std::uint8_t channels;
} pa_sample_spec;

typedef struct pa_buffer_attr {
  std::uint32_t maxlength;
  std::uint32_t tlength;
  std::uint32_t prebuf;
  std::uint32_t minreq;
  std::uint32_t fragsize;
} pa_buffer_attr;

typedef struct pa_channel_map {
  std::uint8_t channels;
  pa_channel_position_t map[PA_CHANNELS_MAX];
} pa_channel_map;

typedef struct pa_cvolume {
  std::uint8_t channels;
  pa_volume_t values[PA_CHANNELS_MAX];
} pa_cvolume;

typedef struct pa_timing_info {
  struct timeval timestamp;
  int synchronized_clocks;
  pa_usec_t sink_usec;
  pa_usec_t source_usec;
  pa_usec_t transport_usec;
  int playing;
  int write_index_corrupt;
  std::int64_t write_index;
  int read_index_corrupt;
  std::int64_t read_index;
  pa_usec_t configured_sink_usec;
  pa_usec_t configured_source_usec;
  std::int64_t since_underrun;
} pa_timing_info;

extern "C" {

typedef struct pa_threaded_mainloop pa_threaded_mainloop;
typedef struct pa_context pa_context;
typedef struct pa_stream pa_stream;
typedef struct pa_operation pa_operation;
typedef struct pa_proplist pa_proplist;
typedef struct pa_format_info pa_format_info;
typedef struct pa_spawn_api pa_spawn_api;
typedef struct pa_io_event pa_io_event;
typedef struct pa_time_event pa_time_event;
typedef struct pa_defer_event pa_defer_event;
typedef struct pa_mainloop_api pa_mainloop_api;

typedef struct pa_sink_input_info {
  std::uint32_t index;
  const char* name;
  std::uint32_t owner_module;
  std::uint32_t client;
  std::uint32_t sink;
  pa_sample_spec sample_spec;
  pa_channel_map channel_map;
  pa_cvolume volume;
  pa_usec_t buffer_usec;
  pa_usec_t sink_usec;
  const char* resample_method;
  const char* driver;
  int mute;
  pa_proplist* proplist;
  int corked;
  int has_volume;
  int volume_writable;
  pa_format_info* format;
} pa_sink_input_info;

typedef void (*pa_io_event_cb_t)(pa_mainloop_api* a, pa_io_event* e, int fd, pa_io_event_flags_t events,
                                 void* userdata);
typedef void (*pa_io_event_destroy_cb_t)(pa_mainloop_api* a, pa_io_event* e, void* userdata);
typedef void (*pa_time_event_cb_t)(pa_mainloop_api* a, pa_time_event* e, const struct timeval* tv, void* userdata);
typedef void (*pa_time_event_destroy_cb_t)(pa_mainloop_api* a, pa_time_event* e, void* userdata);
typedef void (*pa_defer_event_cb_t)(pa_mainloop_api* a, pa_defer_event* e, void* userdata);
typedef void (*pa_defer_event_destroy_cb_t)(pa_mainloop_api* a, pa_defer_event* e, void* userdata);

struct pa_mainloop_api {
  void* userdata;
  pa_io_event* (*io_new)(pa_mainloop_api* a, int fd, pa_io_event_flags_t events, pa_io_event_cb_t cb,
                         void* userdata);
  void (*io_enable)(pa_io_event* e, pa_io_event_flags_t events);
  void (*io_free)(pa_io_event* e);
  void (*io_set_destroy)(pa_io_event* e, pa_io_event_destroy_cb_t cb);
  pa_time_event* (*time_new)(pa_mainloop_api* a, const struct timeval* tv, pa_time_event_cb_t cb, void* userdata);
  void (*time_restart)(pa_time_event* e, const struct timeval* tv);
  void (*time_free)(pa_time_event* e);
  void (*time_set_destroy)(pa_time_event* e, pa_time_event_destroy_cb_t cb);
  pa_defer_event* (*defer_new)(pa_mainloop_api* a, pa_defer_event_cb_t cb, void* userdata);
  void (*defer_enable)(pa_defer_event* e, int b);
  void (*defer_free)(pa_defer_event* e);
  void (*defer_set_destroy)(pa_defer_event* e, pa_defer_event_destroy_cb_t cb);
  void (*quit)(pa_mainloop_api* a, int retval);
};

typedef void (*pa_context_notify_cb_t)(pa_context* c, void* userdata);
typedef void (*pa_context_success_cb_t)(pa_context* c, int success, void* userdata);
typedef void (*pa_context_subscribe_cb_t)(pa_context* c, pa_subscription_event_type_t t, std::uint32_t idx,
                                          void* userdata);
typedef void (*pa_sink_input_info_cb_t)(pa_context* c, const pa_sink_input_info* i, int eol, void* userdata);
typedef void (*pa_stream_notify_cb_t)(pa_stream* p, void* userdata);
typedef void (*pa_stream_success_cb_t)(pa_stream* s, int success, void* userdata);
typedef void (*pa_free_cb_t)(void* p);

// threaded mainloop
CADENCE_PA_EXPORT pa_threaded_mainloop* pa_threaded_mainloop_new(void);
CADENCE_PA_EXPORT void pa_threaded_mainloop_free(pa_threaded_mainloop* m);
CADENCE_PA_EXPORT int pa_threaded_mainloop_start(pa_threaded_mainloop* m);
CADENCE_PA_EXPORT void pa_threaded_mainloop_stop(pa_threaded_mainloop* m);
CADENCE_PA_EXPORT void pa_threaded_mainloop_lock(pa_threaded_mainloop* m);
CADENCE_PA_EXPORT void pa_threaded_mainloop_unlock(pa_threaded_mainloop* m);
CADENCE_PA_EXPORT void pa_threaded_mainloop_wait(pa_threaded_mainloop* m);
CADENCE_PA_EXPORT void pa_threaded_mainloop_signal(pa_threaded_mainloop* m, int wait_for_accept);
CADENCE_PA_EXPORT pa_mainloop_api* pa_threaded_mainloop_get_api(pa_threaded_mainloop* m);

// context
CADENCE_PA_EXPORT pa_context* pa_context_new(pa_mainloop_api* mainloop, const char* name);
CADENCE_PA_EXPORT void pa_context_unref(pa_context* c);
CADENCE_PA_EXPORT int pa_context_connect(pa_context* c, const char* server, pa_context_flags_t flags,
                                         const pa_spawn_api* api);
CADENCE_PA_EXPORT void pa_context_disconnect(pa_context* c);
CADENCE_PA_EXPORT int pa_context_errno(const pa_context* c);
CADENCE_PA_EXPORT pa_context_state_t pa_context_get_state(const pa_context* c);
CADENCE_PA_EXPORT void pa_context_set_state_callback(pa_context* c, pa_context_notify_cb_t cb, void* userdata);

// the client's sink input
CADENCE_PA_EXPORT pa_operation* pa_context_subscribe(pa_context* c, pa_subscription_mask_t m,
                                                     pa_context_success_cb_t cb, void* userdata);
CADENCE_PA_EXPORT void pa_context_set_subscribe_callback(pa_context* c, pa_context_subscribe_cb_t cb, void* userdata);
CADENCE_PA_EXPORT pa_operation* pa_context_set_sink_input_volume(pa_context* c, std::uint32_t idx,
                                                                 const pa_cvolume* volume, pa_context_success_cb_t cb,
                                                                 void* userdata);
CADENCE_PA_EXPORT pa_operation* pa_context_get_sink_input_info(pa_context* c, std::uint32_t idx,
                                                               pa_sink_input_info_cb_t cb, void* userdata);

// operations
CADENCE_PA_EXPORT pa_operation_state_t pa_operation_get_state(const pa_operation* o);
CADENCE_PA_EXPORT void pa_operation_unref(pa_operation* o);

// property lists
CADENCE_PA_EXPORT pa_proplist* pa_proplist_new(void);
CADENCE_PA_EXPORT void pa_proplist_free(pa_proplist* p);
CADENCE_PA_EXPORT int pa_proplist_sets(pa_proplist* p, const char* key, const char* value);

// playback streams
CADENCE_PA_EXPORT pa_stream* pa_stream_new(pa_context* c, const char* name, const pa_sample_spec* ss,
                                           const pa_channel_map* map);
CADENCE_PA_EXPORT pa_stream* pa_stream_new_with_proplist(pa_context* c, const char* name, const pa_sample_spec* ss,
                                                         const pa_channel_map* map, pa_proplist* p);
CADENCE_PA_EXPORT void pa_stream_unref(pa_stream* s);
CADENCE_PA_EXPORT int pa_stream_connect_playback(pa_stream* s, const char* dev, const pa_buffer_attr* attr,
                                                 pa_stream_flags_t flags, const pa_cvolume* volume,
                                                 pa_stream* sync_stream);
CADENCE_PA_EXPORT int pa_stream_disconnect(pa_stream* s);
CADENCE_PA_EXPORT pa_stream_state_t pa_stream_get_state(const pa_stream* s);
CADENCE_PA_EXPORT std::uint32_t pa_stream_get_index(const pa_stream* s);
CADENCE_PA_EXPORT void pa_stream_set_state_callback(pa_stream* s, pa_stream_notify_cb_t cb, void* userdata);
CADENCE_PA_EXPORT void pa_stream_set_started_callback(pa_stream* s, pa_stream_notify_cb_t cb, void* userdata);
CADENCE_PA_EXPORT void pa_stream_set_underflow_callback(pa_stream* s, pa_stream_notify_cb_t cb, void* userdata);
CADENCE_PA_EXPORT int pa_stream_write(pa_stream* p, const void* data, std::size_t nbytes, pa_free_cb_t free_cb,
                                      std::int64_t offset, pa_seek_mode_t seek);
CADENCE_PA_EXPORT std::size_t pa_stream_writable_size(const pa_stream* s);
CADENCE_PA_EXPORT pa_operation* pa_stream_cork(pa_stream* s, int b, pa_stream_success_cb_t cb, void* userdata);
CADENCE_PA_EXPORT int pa_stream_is_corked(const pa_stream* s);
CADENCE_PA_EXPORT pa_operation* pa_stream_flush(pa_stream* s, pa_stream_success_cb_t cb, void* userdata);
CADENCE_PA_EXPORT pa_operation* pa_stream_update_timing_info(pa_stream* p, pa_stream_success_cb_t cb,
                                                             void* userdata);
CADENCE_PA_EXPORT const pa_timing_info* pa_stream_get_timing_info(pa_stream* s);

// helpers
CADENCE_PA_EXPORT const char* pa_strerror(int error);
CADENCE_PA_EXPORT struct timeval* pa_gettimeofday(struct timeval* tv);
CADENCE_PA_EXPORT pa_usec_t pa_timeval_diff(const struct timeval* a, const struct timeval* b);
CADENCE_PA_EXPORT pa_cvolume* pa_cvolume_set(pa_cvolume* a, unsigned channels, pa_volume_t v);
CADENCE_PA_EXPORT pa_volume_t pa_cvolume_avg(const pa_cvolume* a);
CADENCE_PA_EXPORT std::size_t pa_usec_to_bytes(pa_usec_t t, const pa_sample_spec* spec);

}  // extern "C"

#if defined(__LP64__)
// the documented field order on lp64, as libpulse's clients lay these out
static_assert(sizeof(pa_sample_spec) == 12);
static_assert(sizeof(pa_buffer_attr) == 20);
static_assert(sizeof(pa_channel_map) == 132);
static_assert(sizeof(pa_cvolume) == 132);
static_assert(offsetof(pa_timing_info, sink_usec) == 24);
static_assert(offsetof(pa_timing_info, write_index) == 56);
static_assert(offsetof(pa_timing_info, read_index) == 72);
static_assert(sizeof(pa_timing_info) == 104);
static_assert(offsetof(pa_sink_input_info, sample_spec) == 28);
static_assert(offsetof(pa_sink_input_info, volume) == 172);
static_assert(offsetof(pa_sink_input_info, buffer_usec) == 304);
static_assert(offsetof(pa_sink_input_info, proplist) == 344);
static_assert(sizeof(pa_sink_input_info) == 376);
static_assert(sizeof(pa_mainloop_api) == 14 * sizeof(void*));
#endif

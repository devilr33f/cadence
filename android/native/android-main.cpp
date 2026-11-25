// libmain.so: cadence-screen as SDL3's main on android (CadenceActivity), with the routed ui linux runs, and the
// activity's jni: the video surface for SurfaceEmbedOutput (mpv) and the mirror presenter (MediaCodec), the remote's
// assistant button as the screen's assistant key, and the image proxy's https through the platform (ImageFetch)
#include "app/app.h"
#include "app/config-json.h"
#include "app/options.h"
#include "app/routes/core/router.h"
#include "player/mirror-presenter.h"
#include "player/video-fit.h"
#include "player/video-output.h"
#include "stderr-logcat.h"
#include "ui/images/http-fetcher.h"

#include <SDL3/SDL.h>
#include <SDL3/SDL_main.h>
#include <android/native_window_jni.h>
#include <android/log.h>
#include <sys/system_properties.h>
#include <dlfcn.h>
#include <jni.h>

#include <atomic>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <iterator>
#include <mutex>
#include <string>
#include <vector>

namespace {

std::mutex surfaceMu;
// the surface's global ref: mpv's wid, kept until it is destroyed
jobject surfaceRef = nullptr;

JavaVM* vm = nullptr;
// ImageFetch and its get, found on the loading thread (a worker thread's FindClass sees only the system's classes)
jclass fetchClass = nullptr;
jmethodID fetchGet = nullptr;
// CadenceActivity and its videoRect, the video plane's placement (VideoLayout's sink)
jclass activityClass = nullptr;
jmethodID activityVideoRect = nullptr;

// a worker thread attached to the vm once, detached as it ends
struct Attached {
  JNIEnv* env = nullptr;
  ~Attached() {
    if (env != nullptr && vm != nullptr) vm->DetachCurrentThread();
  }
};

JNIEnv* envHere() {
  if (vm == nullptr) return nullptr;
  JNIEnv* env = nullptr;
  if (vm->GetEnv(reinterpret_cast<void**>(&env), JNI_VERSION_1_6) == JNI_OK) return env;
  thread_local Attached attached;
  if (attached.env == nullptr && vm->AttachCurrentThread(&attached.env, nullptr) != JNI_OK) attached.env = nullptr;
  return attached.env;
}

// the image proxy over https, on the cache's worker threads (ImageFetch.get: HttpURLConnection and the system's CAs)
bool platformGet(const std::string& url, std::size_t maxBytes, int timeoutMs, std::string& body, std::string& code) {
  JNIEnv* env = envHere();
  if (env == nullptr || fetchClass == nullptr) {
    code = "tls_unsupported";
    return false;
  }
  jstring jurl = env->NewStringUTF(url.c_str());
  jintArray status = env->NewIntArray(1);
  if (jurl == nullptr || status == nullptr) {
    env->ExceptionClear();
    code = "memory";
    return false;
  }
  auto bytes = static_cast<jbyteArray>(env->CallStaticObjectMethod(
      fetchClass, fetchGet, jurl, static_cast<jint>(maxBytes), static_cast<jint>(timeoutMs), status));
  if (env->ExceptionCheck()) {
    env->ExceptionClear();
    bytes = nullptr;
  }
  jint s = 0;
  env->GetIntArrayRegion(status, 0, 1, &s);
  bool ok = false;
  if (bytes != nullptr) {
    const auto n = env->GetArrayLength(bytes);
    body.resize(static_cast<std::size_t>(n));
    env->GetByteArrayRegion(bytes, 0, n, reinterpret_cast<jbyte*>(body.data()));
    env->DeleteLocalRef(bytes);
    ok = true;
  } else {
    // ImageFetch's codes (its IO, TLS, TIMEOUT, TOO_LARGE), else the http status
    code = s == -2 ? "tls" : s == -3 ? "timeout" : s == -4 ? "too_large" : s == 404 ? "not_found" : s == 406 ? "not_acceptable"
           : s == 502 || s < 0 ? "unavailable" : "status";
  }
  env->DeleteLocalRef(status);
  env->DeleteLocalRef(jurl);
  return ok;
}

// the video plane's rect, from whichever thread shapes it (the main loop, mpv's events, the mirror's presenter):
// CadenceActivity places its SurfaceView on the ui thread
void placeVideo(const cadence::screen::render::Rect& r) {
  JNIEnv* env = envHere();
  if (env == nullptr || activityClass == nullptr) return;
  env->CallStaticVoidMethod(activityClass, activityVideoRect, static_cast<jint>(r.x), static_cast<jint>(r.y),
                            static_cast<jint>(r.width), static_cast<jint>(r.height));
  if (env->ExceptionCheck()) env->ExceptionClear();
}

// the files dir (sdl's internal storage path), where UiAssets put the ui and cadenced keeps its config: set by main
// before anything reads it
std::string filesDir;

// until cadenced's LinkStatus names the server (server_url), the serverUrl enroll wrote to cadenced's config.json
// (Paths.config: files/cadence/config.json, the app's own uid). only that key is read; the token beside it is not
// copied anywhere
std::string enrolledServer() {
  if (filesDir.empty()) return {};
  std::ifstream in(filesDir + "/cadence/config.json", std::ios::binary);
  if (!in) return {};
  const std::string json((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
  return cadence::screen::app::topLevelString(json, "serverUrl").value_or("");
}

// mpv's hwdec=mediacodec is ffmpeg's h264_mediacodec & co, which reach MediaCodec through jni: libavcodec needs the
// vm before the first decoder opens ("No Java virtual machine has been registered", then no video chain at all)
void registerVmWithAvcodec(JavaVM* javaVm) {
  void* avcodec = dlopen("libavcodec.so", RTLD_NOW | RTLD_GLOBAL);
  if (avcodec == nullptr) {
    __android_log_print(ANDROID_LOG_WARN, "cadence-screen", "avcodec_jvm_failed code=dlopen");
    return;
  }
  using SetVm = int (*)(void*, void*);
  auto set = reinterpret_cast<SetVm>(dlsym(avcodec, "av_jni_set_java_vm"));
  if (set == nullptr || set(javaVm, nullptr) < 0) {
    __android_log_print(ANDROID_LOG_WARN, "cadence-screen", "avcodec_jvm_failed code=%s", set == nullptr ? "symbol" : "set");
    return;
  }
  __android_log_print(ANDROID_LOG_INFO, "cadence-screen", "avcodec_jvm_registered");
}

// a G that arrived before sdl ran (the activity recreated under the session's start: onCreate hands the press to
// nativeAssist before the screen's thread exists), held until the event queue can take it
std::atomic<bool> assistHeld{false};

// the assistant key, down and up, as a keyboard's g
void pushAssistKey() {
  for (const bool down : {true, false}) {
    SDL_Event e{};
    e.type = down ? SDL_EVENT_KEY_DOWN : SDL_EVENT_KEY_UP;
    e.key.timestamp = SDL_GetTicksNS();
    e.key.scancode = SDL_SCANCODE_G;
    e.key.key = SDLK_G;
    e.key.down = down;
    SDL_PushEvent(&e);
  }
}

}  // namespace

extern "C" JNIEXPORT jint JNI_OnLoad(JavaVM* javaVm, void*) {
  vm = javaVm;
  JNIEnv* env = nullptr;
  if (vm->GetEnv(reinterpret_cast<void**>(&env), JNI_VERSION_1_6) != JNI_OK) return JNI_ERR;
  registerVmWithAvcodec(vm);
  if (jclass c = env->FindClass("dev/cadence/tv/ImageFetch")) {
    fetchClass = static_cast<jclass>(env->NewGlobalRef(c));
    fetchGet = env->GetStaticMethodID(fetchClass, "get", "(Ljava/lang/String;II[I)[B");
    env->DeleteLocalRef(c);
  }
  if (env->ExceptionCheck() || fetchGet == nullptr) {
    // the screen still runs; images then come over plain http alone
    env->ExceptionClear();
    fetchClass = nullptr;
  }
  if (jclass c = env->FindClass("dev/cadence/tv/CadenceActivity")) {
    activityClass = static_cast<jclass>(env->NewGlobalRef(c));
    activityVideoRect = env->GetStaticMethodID(activityClass, "videoRect", "(IIII)V");
    env->DeleteLocalRef(c);
  }
  if (env->ExceptionCheck() || activityVideoRect == nullptr) {
    // video then fills the screen, stretched as before
    env->ExceptionClear();
    activityClass = nullptr;
    __android_log_print(ANDROID_LOG_WARN, "cadence-screen", "video_rect_unavailable");
  } else {
    cadence::screen::player::videoLayout().setSink(placeVideo);
  }
  return JNI_VERSION_1_6;
}

int main(int argc, char** argv) {
  cadence::android::stderrToLogcat("cadence-screen");
  std::vector<std::string> args(argv + 1, argv + argc);
  std::string error;
  // the default socket on android is cadenced's abstract @cadence-screen
  auto options = cadence::screen::app::parseOptions(args, nullptr, &error);
  if (!options) {
    std::fprintf(stderr, "cadence-screen: bad argument %s\n", error.c_str());
    return 2;
  }
  // the events subsystem alone, so a G held from before sdl ran can queue; the app's loop pumps it once it starts.
  // the platform's own SDL_Init(VIDEO|EVENTS) counts up on top of this one
  if (SDL_Init(SDL_INIT_EVENTS)) {
    if (assistHeld.exchange(false, std::memory_order_relaxed)) pushAssistKey();
  }
  // the ui's rcss and fonts, which UiAssets copied out of the apk
  if (const char* dir = SDL_GetAndroidInternalStoragePath(); dir != nullptr) filesDir = dir;
  if (!filesDir.empty()) ::setenv("CADENCE_SCREEN_UI_ASSETS", (filesDir + "/ui").c_str(), 1);
  // the visualizer's milkdrop presets, which UiAssets copied out of the apk's assets/viz (milkdrop_dir())
  if (!filesDir.empty()) ::setenv("CADENCE_SCREEN_MILKDROP", (filesDir + "/viz/milkdrop").c_str(), 1);
  // the frame profile's switches: adb shell setprop debug.cadence.screen.perf 1|gpu, debug.cadence.screen.hud 1
  {
    char perf[PROP_VALUE_MAX] = {};
    char hud[PROP_VALUE_MAX] = {};
    __system_property_get("debug.cadence.screen.perf", perf);
    __system_property_get("debug.cadence.screen.hud", hud);
    cadence::screen::app::applyPerfEnv(*options, perf, hud);
  }
  cadence::screen::routes::Router::Options routed;
  routed.fetcher = std::make_shared<cadence::screen::ui::images::HttpFetcher>(std::string{}, platformGet, enrolledServer);
  cadence::screen::app::App app(std::move(*options), cadence::screen::routes::makeRoutedUi(std::move(routed)));
  return app.run();
}

// the activity's ui thread: surfaceChanged (a surface) and surfaceDestroyed (null), which returns only once mpv and
// MediaCodec have let go of the surface
extern "C" JNIEXPORT void JNICALL Java_dev_cadence_tv_CadenceActivity_nativeVideoSurface(JNIEnv* env, jclass,
                                                                                         jobject surface, jint width,
                                                                                         jint height) {
  std::lock_guard lock(surfaceMu);
  if (surface != nullptr) {
    if (surfaceRef == nullptr || !env->IsSameObject(surfaceRef, surface)) {
      if (surfaceRef != nullptr) {
        cadence::screen::player::androidMirrorWindowChanged(nullptr);
        cadence::screen::player::androidVideoSurfaceDestroyed();
        env->DeleteGlobalRef(surfaceRef);
      }
      surfaceRef = env->NewGlobalRef(surface);
    }
    cadence::screen::player::androidVideoSurfaceChanged(
        static_cast<std::int64_t>(reinterpret_cast<std::intptr_t>(surfaceRef)), width, height);
    // the window's reference goes to the presenter (the same window again is released there)
    cadence::screen::player::androidMirrorWindowChanged(ANativeWindow_fromSurface(env, surface));
    return;
  }
  cadence::screen::player::androidMirrorWindowChanged(nullptr);
  cadence::screen::player::androidVideoSurfaceDestroyed();
  if (surfaceRef != nullptr) env->DeleteGlobalRef(surfaceRef);
  surfaceRef = nullptr;
}

// the remote's G (AssistSession): the key a keyboard's g is to the screen, its assistant; before sdl runs it is held
// for main to hand over
extern "C" JNIEXPORT void JNICALL Java_dev_cadence_tv_CadenceActivity_nativeAssist(JNIEnv*, jclass) {
  if (SDL_WasInit(SDL_INIT_EVENTS) != 0) {
    pushAssistKey();
    return;
  }
  assistHeld.store(true, std::memory_order_relaxed);
}

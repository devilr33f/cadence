// libcadence-daemon.so: audiod's engine in DaemonService's process (AudiodHost), the daemon cadence-audiod would be,
// on its own thread at an abstract socket cadenced dials; its json lines go to logcat. its airplay receiver
// (libairplay) advertises through NsdManager (dnssd-jni.cpp), and its level is STREAM_MUSIC (volume-jni.cpp), both
// set before the engine first runs
#include "audiod/embedded.h"
#include "audiod/system-volume.h"

#if defined(CADENCE_MEDIA_AIRPLAY_UXPLAY)
#include "audiod/airplay-uxplay-receiver.h"

extern "C" const cadence_dnssd_backend_t* cadence_android_dnssd_backend();
#endif

extern "C" const cadence_system_volume_backend_t* cadence_android_system_volume_backend();

#include <android/log.h>
#include <jni.h>

#include <memory>
#include <mutex>
#include <string>
#include <string_view>

namespace {

std::mutex mu;
std::unique_ptr<cadence::audiod::EmbeddedAudiod> audiod;

void toLogcat(std::string_view line) {
  // one json line, its newline dropped
  if (!line.empty() && line.back() == '\n') line.remove_suffix(1);
  const std::string text(line);
  __android_log_write(ANDROID_LOG_INFO, "cadence-audiod", text.c_str());
}

}  // namespace

extern "C" JNIEXPORT jboolean JNICALL Java_dev_cadence_tv_AudiodHost_nativeStart(JNIEnv* env, jclass, jstring socket) {
  std::lock_guard lock(mu);
  if (audiod && audiod->running()) return JNI_TRUE;
  audiod.reset();
  const char* chars = env->GetStringUTFChars(socket, nullptr);
  const std::string name(chars);
  env->ReleaseStringUTFChars(socket, chars);
#if defined(CADENCE_MEDIA_AIRPLAY_UXPLAY)
  // the backend is static in this library, so it outlives every receiver audiod makes
  cadence_audiod_airplay_dnssd(cadence_android_dnssd_backend());
#endif
  // STREAM_MUSIC as the level, where the board's Configure says so (volume-jni.cpp)
  cadence_audiod_system_volume(cadence_android_system_volume_backend());
  audiod = cadence::audiod::EmbeddedAudiod::start(name, toLogcat);
  return audiod ? JNI_TRUE : JNI_FALSE;
}

extern "C" JNIEXPORT void JNICALL Java_dev_cadence_tv_AudiodHost_nativeStop(JNIEnv*, jclass) {
  std::lock_guard lock(mu);
  audiod.reset();
}

extern "C" JNIEXPORT jboolean JNICALL Java_dev_cadence_tv_AudiodHost_nativeRunning(JNIEnv*, jclass) {
  std::lock_guard lock(mu);
  return audiod && audiod->running() ? JNI_TRUE : JNI_FALSE;
}

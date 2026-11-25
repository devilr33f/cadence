// android's STREAM_MUSIC as audiod's level (media/audiod/system-volume.h): the backend reads and writes the stream
// through SystemVolume's static read and write, attaching audiod's thread for the call, and SystemVolume's notices
// reach audiod through cadence_audiod_system_volume_changed. the daemon library sets the backend before audiod
// starts (daemon-jni.cpp); with no class cached yet it reads nothing, and the level stays a software gain
#include "audiod/system-volume.h"

#include <jni.h>

#include <cerrno>

namespace {

JavaVM* vm = nullptr;
jclass bridge = nullptr;
jmethodID readMethod = nullptr;
jmethodID writeMethod = nullptr;

// the calling thread's env, attached for the call when it is not a java thread (audiod's loop)
class Env {
 public:
  Env() {
    if (vm == nullptr) return;
    if (vm->GetEnv(reinterpret_cast<void**>(&env_), JNI_VERSION_1_6) == JNI_EDETACHED) {
      if (vm->AttachCurrentThread(&env_, nullptr) != JNI_OK) env_ = nullptr;
      attached_ = env_ != nullptr;
    }
  }
  ~Env() {
    if (attached_) vm->DetachCurrentThread();
  }
  Env(const Env&) = delete;
  Env& operator=(const Env&) = delete;
  [[nodiscard]] JNIEnv* get() const { return env_; }

 private:
  JNIEnv* env_ = nullptr;
  bool attached_ = false;
};

int readStream(void*, int* index, int* max, int* muted) {
  const Env e;
  JNIEnv* env = e.get();
  if (env == nullptr || bridge == nullptr) return -ENODEV;
  auto* values = static_cast<jintArray>(env->CallStaticObjectMethod(bridge, readMethod));
  if (env->ExceptionCheck()) {
    env->ExceptionClear();
    return -EIO;
  }
  if (values == nullptr) return -ENOTSUP;
  int rc = -EIO;
  if (env->GetArrayLength(values) == 3) {
    jint v[3];
    env->GetIntArrayRegion(values, 0, 3, v);
    *index = v[0];
    *max = v[1];
    *muted = v[2];
    rc = 0;
  }
  env->DeleteLocalRef(values);
  return rc;
}

int writeStream(void*, int index) {
  const Env e;
  JNIEnv* env = e.get();
  if (env == nullptr || bridge == nullptr) return -ENODEV;
  const jboolean ok = env->CallStaticBooleanMethod(bridge, writeMethod, static_cast<jint>(index));
  if (env->ExceptionCheck()) {
    env->ExceptionClear();
    return -EIO;
  }
  return ok == JNI_TRUE ? 0 : -EPERM;
}

const cadence_system_volume_backend_t backend{nullptr, readStream, writeStream};

}  // namespace

extern "C" JNIEXPORT const cadence_system_volume_backend_t* cadence_android_system_volume_backend() { return &backend; }

extern "C" JNIEXPORT void JNICALL Java_dev_cadence_tv_SystemVolume_nativeInit(JNIEnv* env, jclass cls) {
  if (bridge != nullptr) return;
  if (env->GetJavaVM(&vm) != JNI_OK) return;
  auto* global = static_cast<jclass>(env->NewGlobalRef(cls));
  readMethod = env->GetStaticMethodID(global, "read", "()[I");
  writeMethod = env->GetStaticMethodID(global, "write", "(I)Z");
  if (readMethod == nullptr || writeMethod == nullptr) {
    env->ExceptionClear();
    env->DeleteGlobalRef(global);
    return;
  }
  bridge = global;
}

extern "C" JNIEXPORT void JNICALL Java_dev_cadence_tv_SystemVolume_nativeChanged(JNIEnv*, jclass) {
  cadence_audiod_system_volume_changed();
}

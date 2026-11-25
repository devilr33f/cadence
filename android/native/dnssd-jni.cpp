// libairplay's dnssd backend on android (stream E's hook point, media/third_party/libairplay/cadence_dnssd.h): its
// records go to NsdManager through NsdBridge's static registerFromNative and unregisterFromNative. whoever embeds
// libairplay in this process sets it with cadence_dnssd_set_backend(dnssd, cadence_android_dnssd_backend())
#include "cadence_dnssd.h"

#include <jni.h>

#include <cerrno>

namespace {

JavaVM* vm = nullptr;
jclass bridge = nullptr;
jmethodID registerMethod = nullptr;
jmethodID unregisterMethod = nullptr;

// the calling thread's env, attached for the call when it is not a java thread (libairplay's own)
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

int registerService(void*, const char* type, const char* name, uint16_t port, const unsigned char* txt,
                    size_t txtLen) {
  const Env e;
  JNIEnv* env = e.get();
  if (env == nullptr || bridge == nullptr) return -ENODEV;
  jstring jtype = env->NewStringUTF(type);
  jstring jname = env->NewStringUTF(name);
  jbyteArray jtxt = env->NewByteArray(static_cast<jsize>(txtLen));
  if (jtype == nullptr || jname == nullptr || jtxt == nullptr) return -ENOMEM;
  env->SetByteArrayRegion(jtxt, 0, static_cast<jsize>(txtLen), reinterpret_cast<const jbyte*>(txt));
  const jint rc = env->CallStaticIntMethod(bridge, registerMethod, jtype, jname, static_cast<jint>(port), jtxt);
  if (env->ExceptionCheck()) {
    env->ExceptionClear();
    return -EIO;
  }
  env->DeleteLocalRef(jtxt);
  env->DeleteLocalRef(jname);
  env->DeleteLocalRef(jtype);
  return rc;
}

void unregisterService(void*, const char* type) {
  const Env e;
  JNIEnv* env = e.get();
  if (env == nullptr || bridge == nullptr) return;
  jstring jtype = env->NewStringUTF(type);
  if (jtype == nullptr) return;
  env->CallStaticVoidMethod(bridge, unregisterMethod, jtype);
  if (env->ExceptionCheck()) env->ExceptionClear();
  env->DeleteLocalRef(jtype);
}

const cadence_dnssd_backend_t backend{nullptr, registerService, unregisterService};

}  // namespace

extern "C" JNIEXPORT const cadence_dnssd_backend_t* cadence_android_dnssd_backend() { return &backend; }

extern "C" JNIEXPORT jint JNI_OnLoad(JavaVM* javaVm, void*) {
  vm = javaVm;
  JNIEnv* env = nullptr;
  if (vm->GetEnv(reinterpret_cast<void**>(&env), JNI_VERSION_1_6) != JNI_OK) return JNI_ERR;
  jclass local = env->FindClass("dev/cadence/tv/NsdBridge");
  if (local == nullptr) {
    env->ExceptionClear();
    return JNI_VERSION_1_6;
  }
  bridge = static_cast<jclass>(env->NewGlobalRef(local));
  env->DeleteLocalRef(local);
  registerMethod = env->GetStaticMethodID(bridge, "registerFromNative", "(Ljava/lang/String;Ljava/lang/String;I[B)I");
  unregisterMethod = env->GetStaticMethodID(bridge, "unregisterFromNative", "(Ljava/lang/String;)V");
  if (registerMethod == nullptr || unregisterMethod == nullptr) {
    env->ExceptionClear();
    bridge = nullptr;
  }
  return JNI_VERSION_1_6;
}

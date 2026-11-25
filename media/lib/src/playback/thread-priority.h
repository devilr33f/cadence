#pragma once

namespace cadence {

// android's nice levels for audio threads (android.os.Process): what an app may give its own threads, which the
// scheduler then serves ahead of the ui's
inline constexpr int kNiceAudio = -16;
inline constexpr int kNiceUrgentAudio = -19;
// a thread that feeds an audio one (a network receive and decode with a buffer of its own ahead of the mixer)
inline constexpr int kNiceAudioFeed = -10;

// what raising the calling thread's priority came to; never audio
struct ThreadPriority {
  // false where audiod leaves its threads as they are (linux: pickle's and midi's timings are their own baselines)
  bool attempted = false;
  // the thread's nice afterwards, and the errno when the raise was refused (0: it held)
  int nice = 0;
  int error = 0;
};

// on android, sets the calling thread's nice (setpriority on its tid): the stick's four slow cores run the ui's
// gpu-bound frames beside audiod, and a playback thread at the default priority missed aaudio's burst there
ThreadPriority raiseThreadPriority(int nice);

}  // namespace cadence

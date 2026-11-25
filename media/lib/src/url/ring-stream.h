#pragma once

#include "audiod/ring-sequence.h"

#include <chrono>
#include <cstddef>
#include <memory>

namespace cadence::url {

// the defaults are a ring's; tests shorten them
struct RingStreamOptions {
  // no audio this long (opening, a read, a reopen for the next pass) fails the stream as timeout
  std::chrono::milliseconds stall{3000};
  // decoded ahead of the ring, 48 kHz mono
  std::size_t aheadSamples = 4 * 48000;
  // a url whose whole pass fits is kept after it and played again from memory; a longer one is opened again
  std::size_t keepSamples = 60 * 48000;
};

// a ring's url (RingSequence's stream): one decoder thread opens it with ffmpeg, with the request's Authorization
// header, tls verified against its ca file, decodes its best audio stream at 48 kHz (another rate is unsupported)
// mixed down to mono, and keeps up to aheadSamples ahead of the reads. a failure is the url's fixed code, never
// logged here; the playback logs the ring's fallback. the thread is never joined: it holds what it shares and
// lets go of the url at stop, counted among runningDecoders until it finished
std::shared_ptr<audiod::RingStream> openRingStream(audiod::RingStreamRequest request, RingStreamOptions options = {});

// the factory audiod's playback takes: a url that is not http or https fails at once as invalid
audiod::RingStreamFactory ringStreamFactory(RingStreamOptions options = {});

}  // namespace cadence::url

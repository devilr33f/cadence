#pragma once

#include "ipc/codec.h"
#include "ipc/seqpacket.h"

#include <optional>
#include <span>
#include <string>

namespace cadence::screen::test {

// cadenced's end of the screen socket, for tests: listens on a path (or @name), accepts one screen at a time
class FakeCadenced {
 public:
  explicit FakeCadenced(std::string path);
  ~FakeCadenced();

  // waits up to timeoutMs for the screen to connect
  bool accept(int timeoutMs);
  bool send(const ipc::Envelope& envelope);
  bool sendRaw(std::span<const std::uint8_t> frame) { return conn_.send(frame); }
  // the next envelope, waiting up to timeoutMs
  ipc::DecodedEnvelope next(int timeoutMs);
  // the next envelope with that body, skipping others (log lines, states), waiting up to timeoutMs in all
  ipc::DecodedEnvelope nextOf(pb_size_t whichBody, int timeoutMs);
  // drops the connection, as a cadenced restart would
  void drop();
  [[nodiscard]] const std::string& path() const { return path_; }

 private:
  std::string path_;
  int listen_ = -1;
  ipc::SeqpacketSocket conn_;
};

}  // namespace cadence::screen::test

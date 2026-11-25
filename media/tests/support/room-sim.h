#pragma once

#include "sync/calibration.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <optional>
#include <string>
#include <vector>

namespace cadence::sync::calibration::test {

struct Endpoint {
  std::string id;
  double outputS = 0;
  double inputS = 0;
  double x = 0, y = 0;
  double clockOffsetS = 0;
  double clockPpm = 0;
  double micPpm = 0;
  double level = 1;
  bool silent = false;
};

struct Reflection {
  double delayS = 0;
  double gain = 0;
};

struct Room {
  std::vector<Endpoint> endpoints;
  std::vector<Reflection> reflections;
  double rate = 16000;
  std::size_t micChannels = 1;
  double micStepS = 0.00004;
  double snrDb = 50;
  // music-like masking: pink noise low-passed below the ultrasonic band, without the white noise's ultrasonic floor
  bool musicMasking = false;
  double stampJitterS = 0;
  std::uint32_t seed = 7;
  std::optional<double> recordFromS;
  std::optional<double> recordToS;
};

struct Recording {
  std::vector<std::int16_t> pcm;
  std::size_t channels = 1;
  double rate = 16000;
  std::vector<ReadStamp> stamps;
  SampleClock clock;
  std::function<double(double)> toLeader;
};

class RoomSim {
 public:
  RoomSim(Room room, Plan plan);
  [[nodiscard]] Recording record(std::size_t recorder) const;
  [[nodiscard]] double arrivalS(std::size_t slot, std::size_t source, std::size_t recorder,
                                std::size_t channel = 0) const;

 private:
  Room room_;
  Plan plan_;
};

}  // namespace cadence::sync::calibration::test

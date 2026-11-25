#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <vector>

namespace cadence::sync::calibration {

enum class ProbeKind : std::uint8_t {
  // the legacy exponential sweep, for peers that predate coded probes
  Chirp,
  Gold,
};

struct ProbeShape {
  ProbeKind kind = ProbeKind::Gold;
  double bandLoHz = 1000;
  double bandHiHz = 6000;
  // chirps use this; gold derives its length from its 1023 chips and the pulse's sixteen-chip support
  double durationS = 0;
  double fadeS = 0.01;
  // a fraction of full scale on the music input, never a device volume (rule 7)
  double amplitude = 0.1;
};

// the legacy exponential sweep; its amplitude is on the music input, never the device's volume (rule 7)
struct ChirpShape {
  double f0Hz = 800;
  double f1Hz = 7000;
  double durationS = 0.25;
  double fadeS = 0.01;
  double amplitude = 0.1;
};

inline constexpr std::size_t kGoldChips = 1023;
inline constexpr std::size_t kGoldPulseChips = 8;
inline constexpr double kGoldRollOff = 0.25;

// bpsk +1/-1; code k is m1 xor (m2 rotated by k), for any 32-bit k (the plan uses 0..31)
[[nodiscard]] std::array<std::int8_t, kGoldChips> goldCode(std::uint32_t code);
[[nodiscard]] ProbeShape chirpProbe(const ChirpShape& shape);
// 1023 gold chips at 2500 chips/s on a 19.25 khz carrier, at -30 dbfs on the music input (never a volume change)
[[nodiscard]] ProbeShape ultrasonicProbe();
[[nodiscard]] bool ultrasonic(const ProbeShape& shape);
[[nodiscard]] double probeDuration(const ProbeShape& shape);
// sample t seconds after the probe starts; zero outside it and independent of every other sample
[[nodiscard]] double probeAt(const ProbeShape& shape, std::uint32_t code, double tS);
[[nodiscard]] std::vector<float> probeSamples(const ProbeShape& shape, std::uint32_t code, double rate);

// probeAt for a playing output: probeAt rebuilds the code and sums sixteen pulses per sample, far past what a 32-bit
// arm's audio thread can afford at 48 khz. a gold probe's envelope (its chips, pulses, fades and amplitude) is smooth
// next to its chip rate, so it is tabled (sixteen points a chip for ultrasound, eight in band) and read by linear
// interpolation, times the exact carrier. a chirp is cheap and is computed as is. built off the audio thread, read on it
class ProbeTable {
 public:
  ProbeTable(const ProbeShape& shape, std::uint32_t code);
  [[nodiscard]] double at(double tS) const;
  [[nodiscard]] double durationS() const { return durationS_; }

 private:
  ProbeShape shape_;
  std::uint32_t code_ = 0;
  double durationS_ = 0;
  double stepS_ = 0;
  double carrierHz_ = 0;
  std::vector<float> envelope_;
};

}  // namespace cadence::sync::calibration

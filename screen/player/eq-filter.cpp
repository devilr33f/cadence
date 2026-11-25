#include "player/eq-filter.h"

#include <cmath>
#include <cstdio>

namespace cadence::screen::player {

namespace {

std::string num(double v) {
  char buf[32];
  std::snprintf(buf, sizeof buf, "%.3f", v);
  return buf;
}

bool validBand(const EqBand& b) {
  if (!std::isfinite(b.freqHz) || b.freqHz < 20 || b.freqHz > 20000) return false;
  if (!std::isfinite(b.gainDb) || std::fabs(b.gainDb) > 24) return false;
  if (b.q != 0 && (!std::isfinite(b.q) || b.q < 0.1f || b.q > 10)) return false;
  return true;
}

}  // namespace

std::optional<std::string> eqFilter(const EqRequest& eq) {
  if (!eq.preset.empty()) {
    // the presets' bands live in audiod (the speakers' eq); a DIRECT player knows only flat
    if (eq.preset != "flat" || !eq.bands.empty()) return std::nullopt;
    return std::string();
  }
  if (!std::isfinite(eq.preampDb) || std::fabs(eq.preampDb) > 24) return std::nullopt;
  std::string chain;
  const auto add = [&](const std::string& f) {
    if (!chain.empty()) chain += ',';
    chain += f;
  };
  if (eq.preampDb != 0) add("volume=volume=" + num(eq.preampDb) + "dB");
  for (const auto& b : eq.bands) {
    if (!validBand(b)) return std::nullopt;
    const double q = b.q == 0 ? 0.707 : b.q;
    const std::string f = num(b.freqHz);
    switch (b.type) {
      case cadence_screen_v1_EqType_EQ_TYPE_PEAKING:
        add("equalizer=f=" + f + ":t=q:w=" + num(q) + ":g=" + num(b.gainDb));
        break;
      case cadence_screen_v1_EqType_EQ_TYPE_LOW_SHELF:
        add("lowshelf=f=" + f + ":t=q:w=" + num(q) + ":g=" + num(b.gainDb));
        break;
      case cadence_screen_v1_EqType_EQ_TYPE_HIGH_SHELF:
        add("highshelf=f=" + f + ":t=q:w=" + num(q) + ":g=" + num(b.gainDb));
        break;
      case cadence_screen_v1_EqType_EQ_TYPE_LOW_PASS:
        add("lowpass=f=" + f + ":t=q:w=" + num(q));
        break;
      case cadence_screen_v1_EqType_EQ_TYPE_HIGH_PASS:
        add("highpass=f=" + f + ":t=q:w=" + num(q));
        break;
      default:
        return std::nullopt;
    }
  }
  if (chain.empty()) return std::string();
  return "@" + std::string(kEqLabel) + ":lavfi=[" + chain + "]";
}

}  // namespace cadence::screen::player

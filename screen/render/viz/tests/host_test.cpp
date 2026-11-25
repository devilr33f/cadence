// the host's gl-free parts: the timeline of what is heard, the features, the scheduler, the budget, the playlist, the
// strobe check and the transitions' parameters
#include "fixture.h"
#include "host/budget.h"
#include "host/drive_motion.h"
#include "host/features.h"
#include "host/onset.h"
#include "host/pcm_timeline.h"
#include "host/playlist.h"
#include "host/scheduler.h"
#include "host/strobe.h"
#include "host/transition.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <filesystem>
#include <fstream>
#include <vector>

namespace viz = cadence::render::viz;

namespace {

constexpr std::int64_t kSecond = 1'000'000'000;

// stereo frames whose left is their own index, so a window shows exactly which frames it holds
std::vector<float> ramp(long start, long count) {
  std::vector<float> out;
  for (long i = 0; i < count; ++i) {
    out.push_back(static_cast<float>(start + i));
    out.push_back(static_cast<float>(start + i));
  }
  return out;
}

std::int64_t at(long frame) { return 1000 * kSecond + frame * kSecond / 48000; }

}  // namespace

TEST_CASE("the timeline gives the frames heard now, contiguous, whatever the snapshots overlap") {
  viz::PcmTimeline tl(4096, 48000);
  std::vector<float> w(8);
  CHECK_FALSE(tl.window(at(0), w));
  // snapshots of 512 frames every 400: they overlap, and arrive ahead of when they are heard
  for (long s = 0; s <= 3200; s += 400) {
    const auto snap = ramp(s, 512);
    tl.push(snap, 2, 48000, at(s));
  }
  // heard now: frame 1000; the window ends there, not at the newest written (3711)
  REQUIRE(tl.window(at(1000), w));
  for (std::size_t i = 0; i < w.size(); ++i) CHECK(w[i] == static_cast<float>(993 + i));
  // before the first frame: silence
  tl.window(at(3), w);
  CHECK(w[0] == 0.0f);
  CHECK(w[7] == 3.0f);
  // past the newest written: silence, never stale frames
  tl.window(at(5000), w);
  CHECK(w[7] == 0.0f);
}

TEST_CASE("the timeline keeps gaps silent and starts afresh far off its line") {
  viz::PcmTimeline tl(4096, 48000);
  std::vector<float> w(4);
  tl.push(ramp(0, 100), 2, 48000, at(0));
  tl.push(ramp(200, 100), 2, 48000, at(200));
  tl.window(at(201), w);
  CHECK(w[0] == 0.0f);  // frame 198, never written
  CHECK(w[2] == 200.0f);
  // ten seconds later: a new timeline
  tl.push(ramp(7, 100), 2, 48000, at(480000));
  tl.window(at(480003), w);
  CHECK(w[3] == 10.0f);
}

TEST_CASE("a bass tone reads as bass, a kick as beats") {
  viz::AudioFeatures f;
  viz::FrameInputs in;
  int beats = 0;
  // 4 s of the fixture at 30 fps, windows of the 1024 frames heard
  for (int k = 0; k < 120; ++k) {
    const long end = 1600L * k + 1024;
    const auto st = viz::fixture::stream(end - 1024, 1024);
    std::vector<float> mono(1024);
    for (std::size_t i = 0; i < 1024; ++i) mono[i] = 0.5f * (st[2 * i] + st[2 * i + 1]);
    f.update(mono, 48000, 1.0 / 30, in);
    if (in.onset) ++beats;
  }
  // kicks every half second. the windowed path (nothing fed ahead) only has the 1024 samples of each frame, so a
  // kick that falls in the gap between windows is not there to hear
  CHECK(beats >= 4);
  CHECK(beats <= 14);
  float peak = 0;
  for (std::size_t i = 0; i < viz::kFftBins; ++i) peak = std::max(peak, in.fft[i]);
  CHECK(peak > 0.5f);
  // 110 Hz is bin 2 or 3 at 46.9 Hz a bin
  CHECK(std::max(in.fft[2], in.fft[3]) > in.fft[200]);
}

TEST_CASE("silence has no beat and no level") {
  viz::AudioFeatures f;
  viz::FrameInputs in;
  std::vector<float> zero(1024, 0.0f);
  for (int k = 0; k < 30; ++k) {
    f.update(zero, 48000, 1.0 / 30, in);
    CHECK_FALSE(in.beat);
  }
  CHECK(in.fft[10] == 0.0f);
  CHECK(in.bass == 0.0f);
}

TEST_CASE("the scheduler switches on a beat after every_s, without one after max_wait_s, at once on a key") {
  viz::Scheduler s(viz::SchedulerConfig{.every_s = 10, .max_wait_s = 4, .transition_s = 2});
  CHECK_FALSE(s.step(0, false));
  CHECK_FALSE(s.step(5, true));  // too soon
  CHECK_FALSE(s.step(11, false));
  CHECK(s.step(11.5, true) == viz::Scheduler::Move::Next);
  CHECK(s.transitioning(12));
  CHECK(s.progress(12.5) == Catch::Approx(0.5));
  CHECK_FALSE(s.transitioning(13.6));
  // no beat: at 11.5 + 14
  CHECK_FALSE(s.step(25, false));
  CHECK(s.step(25.6, false) == viz::Scheduler::Move::Next);
  s.ask(viz::Scheduler::Move::Prev);
  CHECK(s.step(26, false) == viz::Scheduler::Move::Prev);
  s.set_auto(false);
  CHECK_FALSE(s.step(100, true));
}

TEST_CASE("the budget skips a source over it for its window, a good frame forgives") {
  viz::Budget b(20, 2);
  CHECK_FALSE(b.over(30, 0.0));
  CHECK_FALSE(b.over(30, 1.0));
  CHECK_FALSE(b.over(10, 1.5));
  CHECK_FALSE(b.over(30, 2.0));
  CHECK_FALSE(b.over(30, 3.9));
  CHECK(b.over(30, 4.1));
}

TEST_CASE("the playlist steps, shuffles and remembers slow ones across runs") {
  const auto file = std::filesystem::temp_directory_path() / "cadence-viz-slow.txt";
  std::filesystem::remove(file);
  {
    viz::Playlist p({"a", "b", "c"}, file.string());
    CHECK(p.current() == "a");
    CHECK(p.next() == "b");
    CHECK(p.prev() == "a");
    CHECK(p.prev() == "c");
    p.mark_slow();
    CHECK(p.current() == "a");
    CHECK(p.shuffle(7) == "b");
    CHECK(p.select("a"));
    CHECK_FALSE(p.select("c"));
  }
  viz::Playlist again({"a", "b", "c"}, file.string());
  CHECK(again.slow().contains("c"));
  CHECK(again.next() == "b");
  CHECK(again.next() == "a");
  again.mark_slow();
  again.mark_slow();
  CHECK(again.empty());
  CHECK(again.current().empty());
  std::filesystem::remove(file);
}

TEST_CASE("strobe: more than 3 flashes a second fails, a slow pulse passes") {
  std::vector<double> fast, slow, dim;
  for (int i = 0; i < 60; ++i) {
    // 10 hz full flips at 30 fps
    fast.push_back((i / 3) % 2 ? 0.9 : 0.1);
    // 1 hz breathing
    slow.push_back(0.4 + 0.3 * std::sin(2 * 3.14159 * i / 30.0));
    // fast but tiny swings
    dim.push_back(i % 2 ? 0.30 : 0.25);
  }
  CHECK(viz::check_strobe(fast, 30).strobing);
  CHECK_FALSE(viz::check_strobe(slow, 30).strobing);
  CHECK_FALSE(viz::check_strobe(dim, 30).strobing);
}

TEST_CASE("transition parameters take their defaults from gl-transitions' comments") {
  const auto ps = viz::transition_params(
      "uniform vec2 direction; // = vec2(1.0, -1.0)\n"
      "uniform float smoothness; // = 0.5\n"
      "uniform bool reverse; // = false\n"
      "uniform vec2 center; // = vec2(0.5)\n"
      "uniform int count; // = 10;\n"
      "uniform float plain;\n");
  REQUIRE(ps.size() == 5);
  CHECK(ps[0].name == "direction");
  CHECK(ps[0].values == std::vector<float>{1.0f, -1.0f});
  CHECK(ps[1].values == std::vector<float>{0.5f});
  CHECK(ps[2].values == std::vector<float>{0.0f});
  CHECK(ps[3].values == std::vector<float>{0.5f});
  CHECK(ps[4].values == std::vector<float>{10.0f});
}

namespace {
// a bar of drums at 120 bpm over a held chord: a kick on every beat, a snare (a noise burst) on two and four, a hat
// on every eighth; frame n of it, mono
float drums(long n) {
  constexpr double kPi = 3.14159265358979;
  const double t = static_cast<double>(n) / 48000.0;
  const double beat = std::fmod(t, 0.5), eighth = std::fmod(t, 0.25);
  const auto bar_beat = static_cast<long>(std::floor(t / 0.5)) % 4;
  const auto seed = static_cast<std::uint32_t>(n) * 1664525U + 1013904223U;
  const double noise = static_cast<double>(seed >> 8) / 16777216.0 - 0.5;
  // a chord and a bass line under it, as a song has
  double s = 0.12 * std::sin(2 * kPi * 220 * t) + 0.08 * std::sin(2 * kPi * 330 * t) + 0.18 * std::sin(2 * kPi * 55 * t);
  s += 0.7 * std::exp(-18 * beat) * std::sin(2 * kPi * (50 + 60 * std::exp(-120 * beat)) * t);
  if (bar_beat == 1 || bar_beat == 3) s += 0.35 * std::exp(-25 * beat) * noise;
  s += 0.06 * std::exp(-90 * eighth) * noise;
  return static_cast<float>(1.2 * s);
}
}  // namespace

TEST_CASE("beats: a drum pattern's tempo holds at 120 and its beats land on the kicks, fed ahead as the host does") {
  viz::AudioFeatures f;
  viz::FrameInputs in;
  std::vector<float> mono(1024), ahead;
  int late = 0, onsets = 0, bars = 0;
  float on_kick = 0, off_beat = 0;
  // 12 s at 30 fps; the stream is fed 150 ms ahead of what is heard
  for (int k = 0; k < 360; ++k) {
    const long heard = 1600L * k + 1024;
    for (std::size_t i = 0; i < 1024; ++i) mono[i] = drums(heard - 1023 + static_cast<long>(i));
    ahead.clear();
    for (long n = f.fed(); n < heard + 7200; ++n) ahead.push_back(drums(n));
    f.feed(ahead);
    f.update(mono, 48000, 1.0 / 30, in, heard);
    if (k >= 180) {
      const double t = static_cast<double>(heard) / 48000.0;
      const double off = std::fmod(t + 0.25, 0.5) - 0.25;
      // the off-beat hat is not the beat: its pulse stays well under the kick's
      if (std::abs(off) < 0.04) on_kick = std::max(on_kick, in.kick_pulse);
      if (std::abs(off) > 0.18 && std::abs(off) < 0.32) off_beat = std::max(off_beat, in.kick_pulse);
    }
    if (k < 180 || !in.onset) continue;
    // after 6 s: each beat on its kick. the low end peaks a few dozen ms after the click, inside the beat
    ++onsets;
    bars += in.bar ? 1 : 0;
    const double t = static_cast<double>(heard) / 48000.0;
    const double off = std::fmod(t + 0.25, 0.5) - 0.25;
    if (std::abs(off) > 0.10) ++late;
    // exponential strength: a drum under the usual size is a short impulse, still clearly a kick
    CHECK(in.beat_pulse > 0.4f);
  }
  CHECK(on_kick > 0.6f);
  CHECK(off_beat < 0.45f * on_kick);
  CHECK(in.bpm == Catch::Approx(120).margin(3));
  // 6 s: twelve kicks
  CHECK(onsets >= 11);
  CHECK(onsets <= 13);
  CHECK(late == 0);
  // one beat in four starts a bar
  CHECK(bars >= 2);
  CHECK(bars <= 4);
  CHECK(in.beat_ages[7] >= 0);
  CHECK(in.beat_ages[7] < 0.55f);
  CHECK(in.bass_level > 0.05f);
  CHECK(in.level > 0.0f);
  CHECK(in.level <= 1.0f);
}

TEST_CASE("onsets: silence has none, and a steady tone none after it settles") {
  viz::OnsetTracker t;
  for (int k = 0; k < 120; ++k) {
    t.update(0, 1.0 / 30);
    CHECK_FALSE(t.onset());
  }
  for (int k = 0; k < 300; ++k) t.update(0.04, 1.0 / 30);
  int late = 0;
  for (int k = 0; k < 90; ++k) {
    t.update(0.04, 1.0 / 30);
    late += t.onset() ? 1 : 0;
  }
  CHECK(late == 0);
}

namespace {
// one frame at 60 fps of the stream sample(n) (frame n, mono), fed 150 ms ahead as the host feeds it
template <class Sample>
void step(viz::AudioFeatures& f, viz::FrameInputs& in, long k, const Sample& sample) {
  const long heard = 800L * k + 1024;
  std::vector<float> mono(1024), ahead;
  for (std::size_t i = 0; i < 1024; ++i) mono[i] = sample(heard - 1023 + static_cast<long>(i));
  for (long n = f.fed(); n < heard + 7200; ++n) ahead.push_back(sample(n));
  f.feed(ahead);
  f.update(mono, 48000, 1.0 / 60, in, heard);
}
}  // namespace

TEST_CASE("drive: a steady tone has no kick, and a quieter master's energy reaches a loud one's once it settles") {
  // seconds of the drums at gain (1: a loud master's level), or a held tone; the kicks that punch and the energy
  // over the last 10 s
  const auto run = [](double gain, bool drums_on, double seconds, int& kicks, float& energy_max) {
    viz::AudioFeatures f;
    viz::FrameInputs in;
    kicks = 0;
    energy_max = 0;
    const auto frames = static_cast<long>(seconds * 60);
    const auto sample = [&](long n) {
      const double t = static_cast<double>(n) / 48000.0;
      const double tone = 0.3 * std::sin(2 * 3.14159265 * 220 * t) + 0.1 * std::sin(2 * 3.14159265 * 3000 * t);
      return static_cast<float>(gain * (drums_on ? drums(n) : tone));
    };
    for (long k = 0; k < frames; ++k) {
      step(f, in, k, sample);
      if (k >= frames - 600) {
        kicks += in.kick && in.kick_pulse >= 0.3f ? 1 : 0;
        energy_max = std::max(energy_max, in.energy);
      }
    }
  };
  int kicks = 0;
  float loud = 0, quiet = 0;
  run(1.0, false, 12, kicks, loud);
  CHECK(kicks == 0);
  run(1.0, true, 12, kicks, loud);
  // 10 s: twenty kicks
  CHECK(kicks >= 18);
  CHECK(kicks <= 21);
  // 25 db quieter: 80 s on (30 s before the song's level is trusted, then its references catch up)
  run(0.056, true, 80, kicks, quiet);
  CHECK(kicks >= 18);
  CHECK(quiet > loud * 0.7f);
}

TEST_CASE("drive: a fade-out calms the kicks, and a next song after a gap punches whole again") {
  // the owner: a song fading out still punched like its chorus. 10 s of drums at full level, a 6 s fade of 4 db a
  // second, a second of silence, then the same drums again
  viz::AudioFeatures f;
  viz::FrameInputs in;
  float loud_max = 0, fade_late_max = 0, next_max = 0;
  int fade_late_kicks = 0;
  for (long k = 0; k < 60 * 27; ++k) {
    const double t0 = static_cast<double>(k) / 60.0;
    const auto sample = [](long n) {
      const double t = static_cast<double>(n) / 48000.0;
      double gain = 1.0;
      if (t >= 10 && t < 16) gain = std::pow(10.0, -4.0 * (t - 10) / 20);
      else if (t >= 16 && t < 17) gain = 0;
      return static_cast<float>(gain * drums(n));
    };
    step(f, in, k, sample);
    if (t0 > 5 && t0 < 10) loud_max = std::max(loud_max, in.beat_pulse);
    // 12 db and more under the song
    if (t0 > 13 && t0 < 16) {
      fade_late_max = std::max(fade_late_max, in.beat_pulse);
      // the grid keeps the song's beat through a fade, faint: none of them a punch
      fade_late_kicks += in.kick && in.kick_pulse >= 0.3f ? 1 : 0;
    }
    if (t0 > 22) next_max = std::max(next_max, in.beat_pulse);
  }
  CHECK(loud_max > 0.8f);
  CHECK(fade_late_max < 0.25f * loud_max);
  CHECK(fade_late_kicks <= 1);
  CHECK(next_max > 0.6f);
}

TEST_CASE("drive: a breakdown of soft bass notes without drums does not punch like the drums around it") {
  // the owner's vultures, 43-53 s: bass notes but no kick, whose log flux is a kick's. 12 s of drums, 10 s of the
  // chord with soft bass notes, 6 s of drums again
  viz::AudioFeatures f;
  viz::FrameInputs in;
  float before = 0, breakdown = 0, after = 0;
  int breakdown_kicks = 0;
  constexpr double kPi = 3.14159265358979;
  const auto sample = [&](long n) {
    const double t = static_cast<double>(n) / 48000.0;
    if (t < 12 || t >= 22) return drums(n);
    // a note every 0.43 s, its 30 ms swell and a slow decay, stepping between three pitches, under the chord
    const double ph = std::fmod(t, 0.43);
    const double hz = 50.0 + 12.0 * std::fmod(std::floor(t / 0.43), 3.0);
    const double s = 0.12 * std::sin(2 * kPi * 220 * t) + 0.08 * std::sin(2 * kPi * 330 * t) +
                     0.05 * std::min(1.0, ph / 0.03) * std::exp(-2.5 * ph) * std::sin(2 * kPi * hz * t);
    return static_cast<float>(s);
  };
  for (long k = 0; k < 60 * 28; ++k) {
    const double t0 = static_cast<double>(k) / 60.0;
    step(f, in, k, sample);
    if (t0 > 5 && t0 < 12) before = std::max(before, in.beat_pulse);
    if (t0 > 13 && t0 < 22) {
      breakdown = std::max(breakdown, in.beat_pulse);
      // the grid may keep the beat through it, faint: none of them a punch
      breakdown_kicks += in.kick && in.kick_pulse >= 0.3f ? 1 : 0;
    }
    if (t0 > 23) after = std::max(after, in.beat_pulse);
  }
  CHECK(before > 0.8f);
  CHECK(breakdown < 0.4f * before);
  CHECK(breakdown_kicks <= 1);
  CHECK(after > 0.8f);
}

TEST_CASE("the timeline holds a group's second of latency: snapshots stamped a second ahead are there when heard") {
  viz::PcmTimeline tl;
  const std::int64_t now = 10'000'000'000;
  // 2 s of a constant 0.5, in 2048-frame snapshots each stamped 1 s after it was sent, sent every ~42 ms
  for (int k = 0; k < 48; ++k) {
    std::vector<float> snap(2048 * 2, 0.5f);
    tl.push(snap, 2, 48000, now + 1'000'000'000 + static_cast<std::int64_t>(k) * 2048 * 1'000'000'000 / 48000);
  }
  std::vector<float> win(1024);
  // a little after the first snapshot sounds: it is all there, nothing overwritten by the ones sent since
  REQUIRE(tl.window(now + 1'100'000'000, win));
  CHECK(win.front() == 0.5f);
  CHECK(win.back() == 0.5f);
}

TEST_CASE("drive: a song fading out makes smaller pulses and no beats of its own, never a drop's") {
  viz::AudioFeatures f;
  viz::FrameInputs in;
  float loud_pulse = 0, tail_pulse = 0;
  int tail_beats = 0, loud_beats = 0;
  // 10 s of drums at full level, then 12 s fading 40 db, 60 fps
  const auto sample = [](long n) {
    const double t = static_cast<double>(n) / 48000.0;
    const double gain = t < 10 ? 1.0 : std::pow(10.0, -40.0 * (t - 10) / 12.0 / 20.0);
    return static_cast<float>(gain * drums(n));
  };
  for (long k = 0; k < 22 * 60; ++k) {
    const double t0 = static_cast<double>(k) / 60.0;
    step(f, in, k, sample);
    if (t0 > 4 && t0 < 10) {
      loud_beats += in.onset ? 1 : 0;
      loud_pulse = std::max(loud_pulse, in.beat_pulse);
    }
    if (t0 > 19) {
      tail_beats += in.onset ? 1 : 0;
      tail_pulse = std::max(tail_pulse, in.beat_pulse);
    }
  }
  CHECK(loud_beats >= 10);
  CHECK(loud_pulse > 0.8f);
  // the last 3 s, 30-40 db under: no punch left, and never more beats than the kicks there are
  CHECK(tail_beats <= 6);
  CHECK(tail_pulse < 0.35f);
}

namespace {
// the drum pattern with the kick and the bass line optional: without them the chord and a riser keep the music going,
// which is a build-up, not a fade
float pattern(long n, bool bass) {
  constexpr double kPi = 3.14159265358979;
  const double t = static_cast<double>(n) / 48000.0;
  const double beat = std::fmod(t, 0.5), eighth = std::fmod(t, 0.25);
  const auto bar_beat = static_cast<long>(std::floor(t / 0.5)) % 4;
  const auto seed = static_cast<std::uint32_t>(n) * 1664525U + 1013904223U;
  const double noise = static_cast<double>(seed >> 8) / 16777216.0 - 0.5;
  double s = 0.16 * std::sin(2 * kPi * 220 * t) + 0.12 * std::sin(2 * kPi * 330 * t);
  if (bass) {
    s += 0.18 * std::sin(2 * kPi * 55 * t);
    s += 0.7 * std::exp(-18 * beat) * std::sin(2 * kPi * (50 + 60 * std::exp(-120 * beat)) * t);
    if (bar_beat == 1 || bar_beat == 3) s += 0.35 * std::exp(-25 * beat) * noise;
  } else {
    // a riser through the mids, above the kick's band, climbing while the bass is gone
    const double rise = std::clamp((t - 10.0) / 7.0, 0.0, 1.0);
    s += (0.08 + 0.22 * rise) * std::sin(2 * kPi * (300.0 + 900.0 * rise) * t);
  }
  s += 0.05 * std::exp(-90 * eighth) * noise;
  return static_cast<float>(1.2 * s);
}
}  // namespace

TEST_CASE("drive: a build-up stays the slower part, and the drop hits at once and stays faster") {
  // the owner's invasive: particles raced the build-up (about 1:25) and then sat slow in the drop (1:30). 10 s of
  // drums, 7 s with the bass and the kick taken out, then the drums again
  viz::AudioFeatures f;
  viz::FrameInputs in;
  double build_rate = 0, hit_rate = 0, sustain_rate = 0;
  int build_n = 0, hit_n = 0, sustain_n = 0;
  float build_max = 0, pulse_build = 0, pulse_hit = 0, intensity_build = 0, intensity_drop = 0;
  float bass_build_max = 0, bass_drop_max = 0;
  int drops = 0;
  double drop_at = -1;
  for (long k = 0; k < 26 * 60; ++k) {
    const double t0 = static_cast<double>(k) / 60.0;
    const auto sample = [](long n) {
      const double t = static_cast<double>(n) / 48000.0;
      return pattern(n, !(t >= 10.0 && t < 17.0));
    };
    step(f, in, k, sample);
    const double rate = viz::drive_travel_rate(in);
    if (t0 > 4 && t0 < 10) {
      CHECK(in.build < 0.05f);
      CHECK_FALSE(in.drop);
    }
    // the last of the build-up, once the bass has fallen away
    if (t0 > 14 && t0 < 17) {
      build_rate += rate;
      ++build_n;
      build_max = std::max(build_max, in.build);
      pulse_build = std::max(pulse_build, in.drop_pulse);
      intensity_build = std::max(intensity_build, in.intensity);
      bass_build_max = std::max(bass_build_max, in.bass_level);
      CHECK_FALSE(in.drop);
    }
    if (in.drop && drop_at < 0) drop_at = t0;
    if (drop_at >= 0 && t0 >= drop_at && t0 < drop_at + 0.4) {
      hit_rate += rate;
      ++hit_n;
      pulse_hit = std::max(pulse_hit, in.drop_pulse);
      drops += in.drop ? 1 : 0;
    }
    // the pulse has faded; the section itself is what should still be the fast part
    if (drop_at >= 0 && t0 > drop_at + 1.5 && t0 < drop_at + 4.0) {
      sustain_rate += rate;
      ++sustain_n;
      intensity_drop = std::max(intensity_drop, in.intensity);
      bass_drop_max = std::max(bass_drop_max, in.bass_level);
    }
  }
  REQUIRE(build_n > 0);
  REQUIRE(hit_n > 0);
  REQUIRE(sustain_n > 0);
  build_rate /= static_cast<double>(build_n);
  hit_rate /= static_cast<double>(hit_n);
  sustain_rate /= static_cast<double>(sustain_n);
  INFO("build " << build_max << " rate " << build_rate << " pulse " << pulse_build << " inten " << intensity_build
                << " bass_build " << bass_build_max << " bass_drop " << bass_drop_max);
  INFO("drop at " << drop_at << " hit " << hit_rate << " pulse " << pulse_hit << " drops " << drops);
  INFO("sustain " << sustain_rate << " inten " << intensity_drop);
  CHECK(build_max > 0.35f);
  CHECK(pulse_build < 0.05f);
  CHECK(bass_build_max < 0.05f);
  CHECK(bass_drop_max > 0.4f);
  CHECK(drop_at > 16.5);
  CHECK(drop_at < 18.5);
  CHECK(pulse_hit > 0.8f);
  CHECK(drops >= 1);
  // speed is the average loudness: the drop section is louder than the build. the arrival frame is not a spike,
  // the average has not caught up yet
  CHECK(sustain_rate > build_rate * 1.4);
  CHECK(hit_rate < sustain_rate);
  CHECK(intensity_drop > intensity_build + 0.25f);
  // exponential kick shaping: slight opening kicks stay small, full kicks stay at 1
  CHECK(viz::exponential_strength(0.0f) == Catch::Approx(0.0f));
  CHECK(viz::exponential_strength(1.0f) == Catch::Approx(1.0f));
  CHECK(viz::exponential_strength(0.5f) < 0.15f);
}

TEST_CASE("impact: kicks with a 60 ms attack over heavy sustained bass hit every beat regardless of fps") {
  // ghost! 40-50 s: a heavy bass wall with kicks whose low-band envelope climbs over ~60 ms. detection runs per
  // fixed audio block, so 24, 30 and 60 fps all detect the same beats with no skipped beat
  constexpr double kPi = 3.14159265358979;
  const auto wall_kick = [](long n) {
    const double t = static_cast<double>(n) / 48000.0;
    const double ph = std::fmod(t, 0.48);
    // heavy standing 55 Hz bass + a 60 ms rising kick every 0.48 s (125 bpm)
    const double env = ph < 0.06 ? (ph / 0.06) : std::exp(-12.0 * (ph - 0.06));
    const double s = 0.32 * std::sin(2 * kPi * 55 * t) + 0.55 * env * std::sin(2 * kPi * 55 * t) +
                     0.10 * std::sin(2 * kPi * 220 * t);
    return static_cast<float>(s);
  };
  int first_kicks = -1;
  for (const int fps : {24, 30, 60}) {
    viz::AudioFeatures f;
    viz::FrameInputs in;
    std::vector<float> mono(1024), ahead;
    int kicks = 0;
    double last_hit = -1, max_gap = 0;
    const long step_samples = 48000 / fps;
    for (long k = 0; k < fps * 14; ++k) {
      const long heard = step_samples * k + 1024;
      for (std::size_t i = 0; i < 1024; ++i) mono[i] = wall_kick(heard - 1023 + static_cast<long>(i));
      ahead.clear();
      for (long n = f.fed(); n < heard + 7200; ++n) ahead.push_back(wall_kick(n));
      f.feed(ahead);
      f.update(mono, 48000, 1.0 / static_cast<double>(fps), in, heard);
      const double t = static_cast<double>(k) / static_cast<double>(fps);
      if (t >= 4.0 && in.kick) {
        ++kicks;
        if (last_hit >= 0) max_gap = std::max(max_gap, t - last_hit);
        last_hit = t;
      }
    }
    INFO("fps=" << fps << " kicks=" << kicks << " max_gap=" << max_gap);
    // 10 s at 0.48 s/beat = ~21 beats; every beat hits, no double-fires, no skipped beat (> 0.65 s), same across fps
    CHECK(kicks >= 19);
    CHECK(kicks <= 22);
    CHECK(max_gap < 0.65);
    if (first_kicks < 0) first_kicks = kicks;
    else CHECK(std::abs(kicks - first_kicks) <= 1);
  }
}

TEST_CASE("drive: staccato kicks separated by silence after a loud wall still register proportionally") {
  // ghost! 55-61 s: after a loud wall, gated staccato kicks with silent gaps between them dragged dB-averaged
  // now_db_ to -24 dB and muted every hit. 8 s of full drums, then 6 s of staccato kicks with silence between them
  constexpr double kPi = 3.14159265358979;
  const auto staccato = [](long n) {
    const double t = static_cast<double>(n) / 48000.0;
    if (t < 8.0) return drums(n);
    const double ph = std::fmod(t, 0.5);
    // 110 ms punchy kick followed by 390 ms dead silence
    if (ph > 0.11) return 0.0f;
    const double s = 0.85 * std::exp(-18.0 * ph) * std::sin(2 * kPi * (55.0 + 55.0 * std::exp(-100.0 * ph)) * t);
    return static_cast<float>(s);
  };
  viz::AudioFeatures f;
  viz::FrameInputs in;
  int stac_kicks = 0;
  float wall_max = 0, stac_max = 0;
  for (long k = 0; k < 14 * 60; ++k) {
    const double t0 = static_cast<double>(k) / 60.0;
    step(f, in, k, staccato);
    if (t0 > 3.0 && t0 < 8.0) wall_max = std::max(wall_max, in.kick_pulse);
    if (t0 > 9.0 && t0 < 13.5) {
      if (in.kick) ++stac_kicks;
      stac_max = std::max(stac_max, in.kick_pulse);
    }
  }
  INFO("wall_max=" << wall_max << " stac_kicks=" << stac_kicks << " stac_max=" << stac_max);
  CHECK(stac_kicks >= 7);
  CHECK(stac_max > 0.35f);
  CHECK(stac_max < wall_max);
}

TEST_CASE("drive: a quiet interlude and a kickless synth swell do not fire fake drops before the real drop") {
  // ghost! 132-153.5 s (18 s quiet interlude + -21 dB pad at 150 s) and 202.6 s (kickless 100 Hz synth swell):
  // track_db_ must not collapse during a loud song's interlude, and a kickless swell must wait for the real kick
  constexpr double kPi = 3.14159265358979;
  const auto song = [](long n) {
    const double t = static_cast<double>(n) / 48000.0;
    // 32 s loud wall (past settled=30 s), 16 s quiet interlude (-18 dB), 3 s quiet pad (-16 dB), then real drop at 51 s
    if (t < 32.0 || t >= 51.0) return drums(n);
    if (t < 48.0) return static_cast<float>(0.07 * std::sin(2 * kPi * 330 * t));
    // quiet low pad without kicks at 48-51 s
    return static_cast<float>(0.08 * std::sin(2 * kPi * 95 * t) + 0.08 * std::sin(2 * kPi * 330 * t));
  };
  viz::AudioFeatures f;
  viz::FrameInputs in;
  int early_drops = 0, real_drops = 0;
  for (long k = 0; k < 55 * 60; ++k) {
    const double t0 = static_cast<double>(k) / 60.0;
    step(f, in, k, song);
    if (t0 >= 32.0 && t0 < 50.8 && in.drop) ++early_drops;
    if (t0 >= 50.8 && t0 < 52.5 && in.drop) ++real_drops;
  }
  CHECK(early_drops == 0);
  CHECK(real_drops >= 1);
}

TEST_CASE("drive: metadata track switch resets loudness, drop, and kick state without a silence gap") {
  // gapless skip from a loud track straight into a -22 dB quieter track: on_track() clears heard_loud_, drop_level,
  // and kick peak_ while preserving fed() on the audio timeline
  viz::AudioFeatures f;
  viz::FrameInputs in;
  // 10 s loud drums, 5 s build-up, 3 s loud drop (so heard_loud_=true, drop_level>0.8, peak_ high)
  for (long k = 0; k < 18 * 60; ++k) {
    const auto sample = [](long n) {
      const double t = static_cast<double>(n) / 48000.0;
      return pattern(n, !(t >= 10.0 && t < 15.0));
    };
    step(f, in, k, sample);
  }
  REQUIRE(in.drop_level > 0.7f);
  const std::int64_t fed_before = f.fed();

  // gapless metadata switch into a 22 dB quieter track (gain = 0.08): no silence between tracks
  f.on_track();
  CHECK(f.fed() == fed_before);
  int quiet_kicks = 0;
  float quiet_energy = 0;
  bool cleared_drop = false;
  for (long k = 18 * 60; k < 78 * 60; ++k) {
    const auto sample = [](long n) { return static_cast<float>(0.08 * drums(n)); };
    step(f, in, k, sample);
    if (k == 18 * 60) cleared_drop = in.drop_level < 0.05f;
    // after the quieter track settles (30 s + ramp), its kicks and energy recover to full range
    if (k >= 68 * 60) {
      if (in.kick && in.kick_pulse >= 0.3f) ++quiet_kicks;
      quiet_energy = std::max(quiet_energy, in.energy);
    }
  }
  CHECK(cleared_drop);
  CHECK(quiet_kicks >= 16);
  CHECK(quiet_energy > 0.6f);
}

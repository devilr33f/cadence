#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <vector>

// the screen's frame profile: where a loop turn's time goes, for the screen_perf log line, the hud and the sim's
// `perf` command. numbers only (rule 8). everything here runs on the main thread
namespace cadence::screen::render::perf {

std::int64_t nowNs();

// the work inside a turn's phases that the loop cannot see from outside (the ui library's own calls)
enum class Span : std::uint8_t {
  // rmlui's Context::Update: style, layout, transitions
  RmlUpdate,
  // rmlui's Context::Render: geometry generation and the draw calls into the ui's target
  RmlRender,
  // texture uploads (images, sprites, glyph atlases); count is the uploads
  Upload,
  // the network images' document scan
  ImageScan,
  // pixels made on the ui thread: a local file's decode, a generated sprite
  Decode,
  Count
};

struct SpanTotal {
  std::int64_t ns = 0;
  std::uint32_t count = 0;
};
using Spans = std::array<SpanTotal, static_cast<std::size_t>(Span::Count)>;

// what a frame drew, counted by the ui's renderer
enum class Counter : std::uint8_t {
  // draw calls (geometry), clip masks written to the stencil, layers pushed (filters, masks: a full-target pass each)
  Draws,
  ClipMasks,
  Layers,
  // pixels the draws cover, inside the target and the scissor, in thousands: the fill (overdraw) a gpu pays
  FillKpx,
  // stencil pixels the clip masks cleared, in thousands (a clear mid-frame is a pass of its own on a tiler)
  StencilClearKpx,
  // gradient draws (rmlui's shader decorators: a per-pixel program, the costliest fill a frame has on a weak gpu) and
  // the pixels they cover, in thousands
  Gradients,
  GradientKpx,
  Count
};
inline constexpr std::size_t kCounters = static_cast<std::size_t>(Counter::Count);
using Counters = std::array<std::uint64_t, kCounters>;

// the spans of the turn now running; take() hands them over and starts the next turn's
void add(Span span, std::int64_t ns, std::uint32_t count = 1);
void count(Counter counter, std::uint64_t n);
Spans take();
Counters takeCounters();

class Scope {
 public:
  explicit Scope(Span span) : span_(span), start_(nowNs()) {}
  ~Scope() { add(span_, nowNs() - start_); }
  Scope(const Scope&) = delete;
  Scope& operator=(const Scope&) = delete;

 private:
  Span span_;
  std::int64_t start_;
};

// a turn's phases as the loop measures them
enum class Phase : std::uint8_t {
  // sdl's events and the keys they made (the wait itself is not counted)
  Events,
  // the link's messages, the tap's rings, the player's pump
  Link,
  // UiLayer::update (rmlui's update inside it)
  Update,
  // composing the frame: the video quad, the ui's render into its target and the composite (cpu side)
  Draw,
  // glFinish before the swap when the gpu is measured (CADENCE_SCREEN_PERF=gpu): the gpu's own time
  Gpu,
  // the swap: the driver's queue, and the vsync wait when the gpu is not measured separately
  Swap,
  Count
};
inline constexpr std::size_t kPhases = static_cast<std::size_t>(Phase::Count);

struct Turn {
  std::array<std::int64_t, kPhases> ns{};
  Spans spans{};
  Counters counters{};
  // a frame was swapped
  bool presented = false;
  // it followed the frame before at once (an animation), not an idle wait
  bool chained = false;
  // the ui changed in this turn and asked for a frame
  bool uiDirty = false;
  // when the turn's work ended (the swap returned)
  std::int64_t endNs = 0;
  // set by the recorder: a chained frame later than 1.5 refresh periods after the one before
  bool late = false;
};

struct Quantiles {
  double p50 = 0, p95 = 0, p99 = 0, max = 0;
};

struct Summary {
  double seconds = 0;
  // loop turns (wakeups) and presented frames in the window, and their rates
  std::uint64_t turns = 0;
  std::uint64_t frames = 0;
  double turnsPerS = 0;
  double fps = 0;
  // chained frames (an animation's), their rate over their own intervals, and those later than 1.5 periods
  std::uint64_t chained = 0;
  double chainedFps = 0;
  std::uint64_t late = 0;
  // the work of a presented frame: its turn's phases summed, without the swap (cpu), and with it
  Quantiles cpuMs;
  Quantiles frameMs;
  // each phase over presented frames, ms
  std::array<Quantiles, kPhases> phaseMs{};
  // spans over presented frames, ms; and their totals over every turn
  std::array<Quantiles, static_cast<std::size_t>(Span::Count)> spanMs{};
  std::array<double, static_cast<std::size_t>(Span::Count)> spanTotalMs{};
  std::array<std::uint64_t, static_cast<std::size_t>(Span::Count)> spanCount{};
  // each counter over presented frames
  std::array<Quantiles, kCounters> counters{};
  // turns that did work without presenting (the ui updated, nothing changed to draw)
  std::uint64_t idleTurns = 0;
};

// the turns since the last reset (the presented ones in a ring of the last `window`); one thread
class Recorder {
 public:
  explicit Recorder(std::size_t window = 600, double refreshHz = 60.0);

  void add(const Turn& turn);
  [[nodiscard]] Summary summary(std::int64_t nowNs) const;
  void reset(std::int64_t nowNs);
  void setRefreshHz(double hz);
  [[nodiscard]] double periodMs() const { return periodNs_ / 1e6; }

  // the last presented frames' totals, oldest first (the hud's graph): cpu ms and swap ms
  struct Bar {
    float cpuMs;
    float swapMs;
    bool late;
  };
  [[nodiscard]] std::vector<Bar> recent(std::size_t n) const;
  // the n presented frames with the most cpu work, worst first (where a stutter's time went)
  [[nodiscard]] std::vector<Turn> worst(std::size_t n) const;

 private:
  std::size_t window_;
  double periodNs_;
  std::vector<Turn> frames_;
  std::size_t next_ = 0;
  std::int64_t sinceNs_ = 0;
  std::uint64_t turns_ = 0;
  std::uint64_t presented_ = 0;
  std::uint64_t idleTurns_ = 0;
  std::array<SpanTotal, static_cast<std::size_t>(Span::Count)> spanTotals_{};
  std::int64_t lastEndNs_ = 0;
  std::uint64_t chained_ = 0;
  double chainedNs_ = 0;
  std::uint64_t late_ = 0;
};

const char* phaseName(Phase p);
const char* spanName(Span s);
const char* counterName(Counter c);

}  // namespace cadence::screen::render::perf

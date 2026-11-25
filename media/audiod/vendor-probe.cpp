#include "audiod/vendor-probe.h"

#include "audio/stock-paths.h"
#include "audiod/mic-layout.h"

#include <pb_decode.h>
#include <pb_encode.h>
#include <signal.h>

#include <array>
#include <cstddef>
#include <filesystem>
#include <utility>

extern char** environ;

namespace cadence::audiod {

namespace {

constexpr int kProbeOpened = 0;
constexpr int kProbeFailed = 1;
constexpr int kProbeUsage = 2;

constexpr std::string_view kHexDigits = "0123456789abcdef";

std::string toHex(std::span<const std::uint8_t> bytes) {
  std::string hex;
  hex.reserve(bytes.size() * 2);
  for (const auto byte : bytes) {
    hex.push_back(kHexDigits[byte >> 4]);
    hex.push_back(kHexDigits[byte & 0x0f]);
  }
  return hex;
}

std::optional<std::vector<std::uint8_t>> fromHex(std::string_view hex) {
  if (hex.size() % 2 != 0)
    return std::nullopt;
  std::vector<std::uint8_t> bytes;
  bytes.reserve(hex.size() / 2);
  for (std::size_t i = 0; i < hex.size(); i += 2) {
    const auto high = kHexDigits.find(hex[i]);
    const auto low = kHexDigits.find(hex[i + 1]);
    if (high == std::string_view::npos || low == std::string_view::npos)
      return std::nullopt;
    bytes.push_back(static_cast<std::uint8_t>(high << 4 | low));
  }
  return bytes;
}

std::string encodeCapture(const cadence_audiod_v1_CaptureConfig& config) {
  std::array<std::uint8_t, cadence_audiod_v1_CaptureConfig_size> buffer{};
  auto stream = pb_ostream_from_buffer(buffer.data(), buffer.size());
  // the section arrived through the same messages, so it always fits
  if (!pb_encode(&stream, &cadence_audiod_v1_CaptureConfig_msg, &config))
    return {};
  return toHex(std::span(buffer).first(stream.bytes_written));
}

std::optional<cadence_audiod_v1_CaptureConfig> decodeCapture(std::string_view hex) {
  const auto bytes = fromHex(hex);
  if (!bytes || bytes->empty())
    return std::nullopt;
  cadence_audiod_v1_CaptureConfig config = cadence_audiod_v1_CaptureConfig_init_zero;
  auto stream = pb_istream_from_buffer(bytes->data(), bytes->size());
  if (!pb_decode(&stream, &cadence_audiod_v1_CaptureConfig_msg, &config))
    return std::nullopt;
  return config;
}

std::string_view stageName(bool commands) { return commands ? "commands" : "main"; }

ProbeVerdict verdictOf(JailedExit ended) {
  if (ended.signal != 0)
    return ProbeVerdict::Failed;
  if (ended.code == kProbeOpened)
    return ProbeVerdict::Passed;
  return ended.code == kProbeSpotterFailed ? ProbeVerdict::SpotterFailed : ProbeVerdict::Failed;
}

std::string_view resultName(ProbeVerdict verdict) {
  switch (verdict) {
    case ProbeVerdict::Passed:
      return "passed";
    case ProbeVerdict::SpotterFailed:
      return "spotter_failed";
    case ProbeVerdict::TimedOut:
      return "timeout";
    default:
      return "failed";
  }
}

}  // namespace

ProbeProgram selfProbe() {
  ProbeProgram program{.path = "/proc/self/exe", .env = {}};
  for (char** variable = environ; variable != nullptr && *variable != nullptr; ++variable)
    program.env.emplace_back(*variable);
  return program;
}

Result<VendorSetup, std::string_view> resolveVendor(const cadence_audiod_v1_CaptureConfig& config,
                                                    std::uint64_t warmupMs) {
  const auto layout = boardLayout(config);
  if (!layout.ok())
    return std::string_view("layout_invalid");
  const auto& engines = config.engines;
  std::vector<std::filesystem::path> roots(engines.stock_roots, engines.stock_roots + engines.stock_roots_count);
  const auto root = resolveStockRoot(roots, directoryExists);
  if (!root || engines.library[0] == '\0')
    return std::string_view("library_missing");
  if (engines.activation_model[0] == '\0')
    return std::string_view("model_missing");
  VendorSetup setup{.stock = {.paths = stockPathsUnder(*root, engines.library, engines.activation_model),
                              .preset = engines.vqe_preset,
                              .capture = layout.value(),
                              .outputSampleRate = 16000,
                              .wakeWarmupMs = warmupMs,
                              .commandWords = false},
                    .commandCode = {}};
  if (const auto missing = firstMissingStockPath(setup.stock.paths, pathExists))
    return std::string_view(*missing == setup.stock.paths.library ? "library_missing" : "model_missing");
  // words to match are what command words are for: without them the command model is left alone
  if (engines.command_model[0] != '\0' && engines.command_words_count > 0) {
    const std::filesystem::path model(engines.command_model);
    if (directoryExists(model)) {
      setup.stock.paths.commandModel = model;
      setup.stock.commandWords = true;
    } else {
      setup.commandCode = "command_model_missing";
    }
  }
  return setup;
}

std::vector<std::string> probeArgs(const cadence_audiod_v1_CaptureConfig& config, bool commands) {
  std::vector<std::string> args{std::string(kProbeFlag), encodeCapture(config)};
  if (commands)
    args.emplace_back(kProbeCommandsFlag);
  return args;
}

int vendorProbeMain(std::span<char* const> argv, const StockLibraryLoader& loader) {
  if (argv.size() < 3 || argv.size() > 4 || std::string_view(argv[1]) != kProbeFlag)
    return kProbeUsage;
  const bool commands = argv.size() == 4;
  if (commands && std::string_view(argv[3]) != kProbeCommandsFlag)
    return kProbeUsage;
  const auto config = decodeCapture(argv[2]);
  if (!config)
    return kProbeUsage;
  auto setup = resolveVendor(*config, 0);
  if (!setup.ok() || (commands && !setup.value().stock.commandWords))
    return kProbeFailed;
  auto stock = setup.value().stock;
  stock.commandWords = commands;
  StockAudio audio(loader);
  const auto info = audio.initialize(stock);
  if (!info.ok())
    return kProbeFailed;
  if (!info.value().spotter)
    return commands ? kProbeFailed : kProbeSpotterFailed;
  if (commands && !info.value().spotter->commands)
    return kProbeFailed;
  return kProbeOpened;
}

VendorProbe::VendorProbe(Loop& loop, Done done) : loop_(loop), done_(std::move(done)) {}

std::unique_ptr<VendorProbe> VendorProbe::start(Loop& loop, const Log& log, const ProbeProgram& program,
                                                std::vector<std::string> args, std::chrono::milliseconds limit,
                                                Done done) {
  std::unique_ptr<VendorProbe> probe(new VendorProbe(loop, std::move(done)));
  auto* self = probe.get();
  const std::weak_ptr<char> alive = probe->alive_;
  // the child's own environment, nothing added: no nice or oom score of its own
  const PlainPlan plan{.program = program.path, .env = program.env, .nice = 0, .oomScoreAdj = {}};
  auto started = JailedProcess::startPlain(log, plan, std::move(args), [&loop, self, alive](JailedExit ended) {
    loop.post([self, alive, ended] {
      if (alive.lock())
        self->finish({.verdict = verdictOf(ended), .exit = ended});
    });
  });
  if (!started.ok()) {
    loop.post([self, alive] {
      if (alive.lock())
        self->finish({.verdict = ProbeVerdict::Failed, .exit = std::nullopt});
    });
    return probe;
  }
  probe->process_ = std::move(started.value());
  probe->timer_ = loop.after(limit, [self] {
    self->timer_ = 0;
    self->process_->kill(SIGKILL);
    self->finish({.verdict = ProbeVerdict::TimedOut, .exit = std::nullopt});
  });
  return probe;
}

VendorProbe::~VendorProbe() {
  if (timer_ != 0)
    loop_.cancel(timer_);
}

void VendorProbe::finish(ProbeOutcome outcome) {
  if (finished_)
    return;
  finished_ = true;
  if (timer_ != 0) {
    loop_.cancel(timer_);
    timer_ = 0;
  }
  const auto done = done_;
  done(outcome);
}

VendorResolver::VendorResolver(Loop& loop, const Log& log, const VendorOptions& options)
    : loop_(loop), log_(log), options_(options) {}

void VendorResolver::resolve(const cadence_audiod_v1_CaptureConfig& config, Done done) {
  cancel();
  done_ = std::move(done);
  auto setup = resolveVendor(config, options_.wakeWarmupMs);
  if (!setup.ok()) {
    log_.warn("vendor_engines_missing", {{"code", setup.error()}});
    return finish({.setup = std::nullopt, .code = setup.error()});
  }
  config_ = config;
  setup_ = std::move(setup.value());
  probe(false);
}

void VendorResolver::cancel() {
  probe_.reset();
  done_ = nullptr;
  config_.reset();
  setup_.reset();
}

void VendorResolver::probe(bool commands) {
  auto args = probeArgs(*config_, commands);
  auto key = args[1] + (commands ? "+commands" : "");
  if (const auto known = verdicts_.find(key); known != verdicts_.end())
    return probed(commands, known->second);
  probe_ = VendorProbe::start(loop_, log_, options_.probe, std::move(args), options_.probeLimit,
                              [this, commands, key = std::move(key)](ProbeOutcome outcome) {
                                verdicts_[key] = outcome.verdict;
                                const auto ended = outcome.exit.value_or(JailedExit{.code = -1, .signal = 0});
                                log_.info("vendor_probe", {{"stage", stageName(commands)},
                                                           {"result", resultName(outcome.verdict)},
                                                           {"exit", ended.code},
                                                           {"signal", ended.signal}});
                                probed(commands, outcome.verdict);
                              });
}

void VendorResolver::probed(bool commands, ProbeVerdict verdict) {
  if (!commands) {
    if (verdict == ProbeVerdict::SpotterFailed) {
      auto setup = *setup_;
      setup.stock.commandWords = false;
      setup.stock.paths.commandModel.reset();
      return finish({.setup = std::move(setup), .code = "spotter_failed"});
    }
    if (verdict != ProbeVerdict::Passed)
      return finish({.setup = std::nullopt,
                     .code = verdict == ProbeVerdict::TimedOut ? "probe_timeout" : "probe_failed"});
    if (!setup_->stock.commandWords)
      return finish({.setup = setup_, .code = setup_->commandCode});
    return probe(true);
  }
  auto setup = *setup_;
  if (verdict == ProbeVerdict::Passed)
    return finish({.setup = std::move(setup), .code = {}});
  setup.stock.commandWords = false;
  setup.stock.paths.commandModel.reset();
  finish({.setup = std::move(setup), .code = "command_model_failed"});
}

void VendorResolver::finish(VendorVerdict verdict) {
  // a probe that just ended may still be the caller: it is released with the next resolve or cancel
  auto done = std::exchange(done_, nullptr);
  config_.reset();
  setup_.reset();
  if (done)
    done(std::move(verdict));
}

}  // namespace cadence::audiod

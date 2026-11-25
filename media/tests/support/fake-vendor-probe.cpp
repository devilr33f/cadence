// a stand-in for cadence-audiod --probe-vendor: the same probe over the fake stock library instead of the device's,
// scripted by the test through the environment:
//   CADENCE_FAKE_PROBE=<quick_exit|abort|hang>@<stock symbol>  that call quick_exit(1)s, aborts or never returns
//   CADENCE_FAKE_COMMAND_MODEL=<dir>                           the dir the fake reads as the command model
//   CADENCE_FAKE_PROBE_LOG=<file>                              each run appends main or commands
//   CADENCE_FAKE_PROBE_DELAY_MS=<ms>                           each run waits this long before it probes
//   CADENCE_FAKE_FAIL=<fake failure name>                      that stock call fails cleanly, as the fake reports it
#include "audio/stock-types.h"
#include "audiod/vendor-probe.h"
#include "support/fake-stock.h"

#include <sys/resource.h>
#include <unistd.h>

#include <chrono>
#include <cstdlib>
#include <fstream>
#include <map>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <thread>

namespace {

namespace abi = cadence::stock_abi;

enum class Action { QuickExit, Abort, Hang };

Action action = Action::Hang;

[[noreturn]] void act() {
  switch (action) {
    case Action::QuickExit:
      std::quick_exit(1);
    case Action::Abort:
      std::abort();
    case Action::Hang:
      break;
  }
  for (;;)
    ::pause();
}

template <typename Function>
struct Crash;

// stands in for one stock call with its exact signature
template <typename Result, typename... Args>
struct Crash<Result(Args...)> {
  static Result call(Args...) { act(); }
};

template <typename Function>
std::pair<const std::string, void*> crash(const char* name) {
  return {name, reinterpret_cast<void*>(&Crash<Function>::call)};
}

const std::map<std::string, void*> kCrashes = {
    crash<abi::VqeConfigCreateFromPreset>("YandexVqeC_Config_CreateFromPreset"),
    crash<abi::VqeCreate>("YandexVqeC_Create"),
    crash<abi::SpotterCreateMultichannel>("spotter_create_multichannel"),
    crash<abi::SpotterCreateFromMultiConf>("spotter_create_from_multi_conf"),
};

// the fake library, but the scripted symbol crashes
class Scripted final : public cadence::StockLibrary {
 public:
  Scripted(std::shared_ptr<cadence::StockLibrary> fake, std::string symbol)
      : fake_(std::move(fake)), symbol_(std::move(symbol)) {}

  void* symbol(const char* name) const override {
    if (name == symbol_)
      return kCrashes.at(symbol_);
    return fake_->symbol(name);
  }

 private:
  std::shared_ptr<cadence::StockLibrary> fake_;
  std::string symbol_;
};

std::string_view env(const char* name) {
  const char* value = std::getenv(name);
  return value == nullptr ? std::string_view() : std::string_view(value);
}

}  // namespace

int main(int argc, char** argv) {
  // an abort leaves no core behind in the build tree
  const rlimit noCore{0, 0};
  ::setrlimit(RLIMIT_CORE, &noCore);

  const std::span<char*> args(argv, static_cast<std::size_t>(argc));
  if (const auto log = env("CADENCE_FAKE_PROBE_LOG"); !log.empty()) {
    const bool commands = args.size() > 3 && std::string_view(args[3]) == cadence::audiod::kProbeCommandsFlag;
    std::ofstream(std::string(log), std::ios::app) << (commands ? "commands" : "main") << '\n';
  }

  std::string symbol;
  if (const auto script = env("CADENCE_FAKE_PROBE"); !script.empty()) {
    const auto at = script.find('@');
    const auto name = script.substr(0, at);
    symbol = std::string(script.substr(at + 1));
    if (at == std::string_view::npos || kCrashes.count(symbol) == 0)
      return 99;
    if (name == "quick_exit")
      action = Action::QuickExit;
    else if (name == "abort")
      action = Action::Abort;
    else if (name != "hang")
      return 99;
  }

  if (const auto delay = env("CADENCE_FAKE_PROBE_DELAY_MS"); !delay.empty())
    std::this_thread::sleep_for(std::chrono::milliseconds(std::atoi(std::string(delay).c_str())));

  cadence::fake_stock::FakeStock stock;
  // a probe of a wider fake preset gets its counts from the rig, as the real preset supplies its own
  if (const auto mics = env("CADENCE_FAKE_PROBE_MICS"); !mics.empty())
    stock.mics = std::atoi(std::string(mics).c_str());
  if (const auto speakers = env("CADENCE_FAKE_PROBE_SPEAKERS"); !speakers.empty())
    stock.speakers = std::atoi(std::string(speakers).c_str());
  if (const auto model = env("CADENCE_FAKE_COMMAND_MODEL"); !model.empty())
    stock.commandModel = model;
  if (const auto fail = env("CADENCE_FAKE_FAIL"); !fail.empty())
    stock.failures[std::string(fail)] = "scripted";
  cadence::fake_stock::serve(&stock);
  const cadence::StockLibraryLoader loader = [&symbol](const std::filesystem::path& path) -> cadence::StockLibraryResult {
    auto fake = cadence::fake_stock::loadFake(path);
    if (!fake.ok() || symbol.empty())
      return fake;
    return std::shared_ptr<cadence::StockLibrary>(std::make_shared<Scripted>(fake.value(), symbol));
  };
  return cadence::audiod::vendorProbeMain(args, loader);
}

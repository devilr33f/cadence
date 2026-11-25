#pragma once

#include "audiod/sys.h"
#include "dbus/bus.h"
#include "dbus/client.h"
#include "support/eventually.h"

#include <catch2/catch_test_macros.hpp>

#include <poll.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <deque>
#include <functional>
#include <future>
#include <mutex>
#include <optional>
#include <set>
#include <string>
#include <thread>
#include <utility>
#include <vector>

// a stand-in for shairport sync's native d-bus service, in the test's process on a private bus, scripted by the test:
// it owns org.gnome.ShairportSync and exports /org/gnome/ShairportSync with org.gnome.ShairportSync (Protocol,
// DropSession) and its RemoteControl (PlayerState, AirplayVolume, Client; Play, Pause, Next, Previous,
// SetAirplayVolume), and records every call. no test ever runs the real receiver or casts
namespace cadence::test_support {

class FakeShairport {
 public:
  struct Properties {
    std::string protocol = "AirPlay 2";
    std::string state = "Not Available";
    double volume = 0;
    std::string client;
    // the first read of PlayerState answers state, but first says it changed to this, as a change racing the read
    // would
    std::optional<std::string> racingState{};
    // the interface with Protocol, and the remote control, are there only this long after the name was taken, as a
    // service that took its name before its objects: reads of them fail until then
    std::chrono::milliseconds mainAfter{0};
    std::chrono::milliseconds remoteAfter{0};
    // the first read of PlayerState answers only this long after it came, the fake busy meanwhile
    std::chrono::milliseconds slowStateRead{0};
  };

  // connected, its interfaces exported (or about to be) and its name owned
  FakeShairport(const std::string& address, Properties initial)
      : properties_(std::move(initial)), race_(properties_.racingState), slowRead_(properties_.slowStateRead) {
    auto connected = dbus::Client::connect(address);
    REQUIRE(connected.ok());
    client_.emplace(std::move(connected.value()));
    unique_ = client_->uniqueName();
    const auto now = std::chrono::steady_clock::now();
    mainAt_ = now + properties_.mainAfter;
    remoteAt_ = now + properties_.remoteAfter;
    exportDue();
    const auto owned = client_->callAndWait(dbus::requestNameCall(std::string(kName), dbus::kNameDoNotQueue));
    REQUIRE(owned.status == dbus::CallStatus::Ok);
    REQUIRE(owned.body.size() == 1);
    REQUIRE(owned.body.front().asUint32() == dbus::kNamePrimaryOwner);
    wake_ = audiod::UniqueFd(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK));
    REQUIRE(wake_.valid());
    thread_ = std::thread([this] { run(); });
  }

  ~FakeShairport() {
    run([this] { stopping_ = true; });
    thread_.join();
  }

  FakeShairport(const FakeShairport&) = delete;
  FakeShairport& operator=(const FakeShairport&) = delete;

  [[nodiscard]] const std::string& uniqueName() const { return unique_; }

  // each sets the property and says so with a PropertiesChanged, as shairport does
  void state(std::string state) {
    run([this, state = std::move(state)] {
      properties_.state = state;
      changed({{"PlayerState", dbus::Value::string(state)}});
    });
  }
  void volume(double db) {
    run([this, db] {
      properties_.volume = db;
      changed({{"AirplayVolume", dbus::Value::float64(db)}});
    });
  }
  void client(std::string id) {
    run([this, id = std::move(id)] {
      properties_.client = id;
      changed({{"Client", dbus::Value::string(id)}});
    });
  }
  void protocol(std::string protocol) {
    run([this, protocol = std::move(protocol)] { properties_.protocol = protocol; });
  }
  // a PropertiesChanged of the remote control as given, whatever it says
  void emit(std::vector<std::pair<std::string, dbus::Value>> entries, std::vector<std::string> invalidated = {}) {
    run([this, entries = std::move(entries), invalidated = std::move(invalidated)] {
      client_->emitPropertiesChanged(std::string(kPath), std::string(kRemote), entries, invalidated);
    });
  }
  // a method of that name answers with an error
  void fails(std::string method) {
    run([this, method = std::move(method)] { failing_.insert(method); });
  }
  // Pause and Play change PlayerState as a receiver that honours them would (by default it ignores them, as an
  // airplay 2 receiver may)
  void honoursControls() {
    run([this] { honours_ = true; });
  }
  void releaseName() {
    run([this] {
      client_->send(dbus::Message::methodCall(std::string(dbus::kBusName), std::string(dbus::kBusPath),
                                              std::string(dbus::kBusInterface), "ReleaseName",
                                              {dbus::Value::string(std::string(kName))}));
    });
  }

  // what was called, in order: the method's name, SetAirplayVolume with its value
  [[nodiscard]] std::vector<std::string> calls() const {
    std::scoped_lock lock(mutex_);
    return calls_;
  }
  [[nodiscard]] std::size_t count(const std::string& call) const {
    const auto all = calls();
    return static_cast<std::size_t>(std::count(all.begin(), all.end(), call));
  }
  bool waitCall(const std::string& call, std::size_t times = 1,
                std::chrono::milliseconds limit = std::chrono::seconds(5)) const {
    return eventually([&] { return count(call) >= times; }, limit);
  }

 private:
  static constexpr std::string_view kName = "org.gnome.ShairportSync";
  static constexpr std::string_view kPath = "/org/gnome/ShairportSync";
  static constexpr std::string_view kMain = "org.gnome.ShairportSync";
  static constexpr std::string_view kRemote = "org.gnome.ShairportSync.RemoteControl";

  // runs task on the fake's thread and waits for it
  void run(std::function<void()> task) {
    std::promise<void> done;
    auto finished = done.get_future();
    {
      std::scoped_lock lock(mutex_);
      tasks_.push_back([&task, &done] {
        task();
        done.set_value();
      });
    }
    const std::uint64_t one = 1;
    REQUIRE(::write(wake_.get(), &one, sizeof one) == sizeof one);
    finished.wait();
  }

  void run() {
    while (!stopping_) {
      pollfd fds[] = {{client_->fd(), POLLIN, 0}, {wake_.get(), POLLIN, 0}};
      ::poll(fds, 2, client_->hasBuffered() ? 0 : 50);
      if ((fds[1].revents & POLLIN) != 0) {
        std::uint64_t ignored = 0;
        [[maybe_unused]] const auto read = ::read(wake_.get(), &ignored, sizeof ignored);
        std::deque<std::function<void()>> tasks;
        {
          std::scoped_lock lock(mutex_);
          tasks.swap(tasks_);
        }
        for (auto& task : tasks)
          task();
      }
      exportDue();
      if (client_->open())
        client_->process();
    }
  }

  void exportDue() {
    const auto now = std::chrono::steady_clock::now();
    if (mainAt_ && now >= *mainAt_) {
      mainAt_.reset();
      exportMain();
    }
    if (remoteAt_ && now >= *remoteAt_) {
      remoteAt_.reset();
      exportRemote();
    }
  }

  void changed(std::vector<std::pair<std::string, dbus::Value>> entries) {
    client_->emitPropertiesChanged(std::string(kPath), std::string(kRemote), std::move(entries));
  }

  dbus::MethodResult answer(const std::string& method, std::function<void()> effect = {}) {
    {
      std::scoped_lock lock(mutex_);
      calls_.push_back(method);
    }
    if (failing_.contains(method))
      return {.values = {}, .errorName = "org.gnome.ShairportSync.Error", .errorText = "refused"};
    if (effect)
      effect();
    return {};
  }

  void exportMain() {
    dbus::Interface main{.name = std::string(kMain), .methods = {}, .properties = {}};
    main.properties["Protocol"] = {.get = [this] { return dbus::Value::string(properties_.protocol); }, .set = {}};
    main.methods["DropSession"] = [this](const dbus::Message&) {
      return answer("DropSession", [this] {
        properties_.state = "Not Available";
        changed({{"PlayerState", dbus::Value::string(properties_.state)}});
      });
    };
    client_->exportInterface(std::string(kPath), std::move(main));
  }

  void exportRemote() {
    dbus::Interface remote{.name = std::string(kRemote), .methods = {}, .properties = {}};
    remote.properties["PlayerState"] = {.get =
                                            [this] {
                                              if (slowRead_.count() > 0)
                                                std::this_thread::sleep_for(std::exchange(slowRead_, {}));
                                              const auto value = dbus::Value::string(properties_.state);
                                              if (race_) {
                                                properties_.state = *std::exchange(race_, std::nullopt);
                                                changed({{"PlayerState", dbus::Value::string(properties_.state)}});
                                              }
                                              return value;
                                            },
                                        .set = {}};
    remote.properties["AirplayVolume"] = {.get = [this] { return dbus::Value::float64(properties_.volume); },
                                          .set = {}};
    remote.properties["Client"] = {.get = [this] { return dbus::Value::string(properties_.client); }, .set = {}};
    const auto honoured = [this](std::string state) {
      return [this, state] {
        if (honours_ && properties_.state != state) {
          properties_.state = state;
          changed({{"PlayerState", dbus::Value::string(state)}});
        }
      };
    };
    remote.methods["Play"] = [this, honoured](const dbus::Message&) { return answer("Play", honoured("Playing")); };
    remote.methods["Pause"] = [this, honoured](const dbus::Message&) { return answer("Pause", honoured("Paused")); };
    remote.methods["Next"] = [this](const dbus::Message&) { return answer("Next"); };
    remote.methods["Previous"] = [this](const dbus::Message&) { return answer("Previous"); };
    remote.methods["SetAirplayVolume"] = [this](const dbus::Message& call) {
      const auto db = call.body.size() == 1 ? call.body.front().asDouble() : std::nullopt;
      char text[32]{};
      std::snprintf(text, sizeof text, "SetAirplayVolume %.2f", db.value_or(-999));
      return answer(text);
    };
    client_->exportInterface(std::string(kPath), std::move(remote));
  }

  // the fake thread's own, once it runs
  Properties properties_;
  std::optional<std::string> race_;
  std::chrono::milliseconds slowRead_;
  std::optional<std::chrono::steady_clock::time_point> mainAt_;
  std::optional<std::chrono::steady_clock::time_point> remoteAt_;
  std::optional<dbus::Client> client_;
  std::string unique_;
  std::set<std::string, std::less<>> failing_;
  bool honours_ = false;
  bool stopping_ = false;

  mutable std::mutex mutex_;
  std::vector<std::string> calls_;
  std::deque<std::function<void()>> tasks_;
  audiod::UniqueFd wake_;
  std::thread thread_;
};

}  // namespace cadence::test_support

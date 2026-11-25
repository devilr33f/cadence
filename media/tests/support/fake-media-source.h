#pragma once

#include "audiod/media-source.h"
#include "backend/pcm-input.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>

#include <atomic>
#include <cstdint>
#include <deque>
#include <list>
#include <map>
#include <memory>
#include <mutex>
#include <span>
#include <string>
#include <string_view>
#include <thread>
#include <utility>
#include <vector>

namespace cadence::test_support {

// how the fake source a url names behaves
struct FakeSourceSpec {
  audiod::MediaKind kind = audiod::MediaKind::Url;
  bool keepsPaused = false;
  audiod::MediaActions actions{audiod::MediaAction::Pause, audiod::MediaAction::Resume, audiod::MediaAction::Stop,
                               audiod::MediaAction::Seek};
  // it reports ended from inside stop, as a source that finishes as it stops may
  bool endsOnStop = false;
  // pause or stop answers failed and changes nothing
  bool pauseFails = false;
  bool stopFails = false;
  // above 0, it plays this tone into its output from a thread of its own while the output is open and it is not
  // paused, as a decoder would
  double toneHz = 0;
  // pause, resume, next and previous from cadenced are answered later, when the test says, as soloist answers them
  bool answersLater = false;
};

// the url sources of a test. "http://fake/<name>" makes the source <name>, as its spec says; "http://fake/refused/
// <code>" is refused with that code. every source call lands in the journal as "<name>:<call>", and a source does
// what it is told at once, reporting from inside the call: pause reports paused, seek the position, stop ended when
// its spec says so, resume and openOutput nothing (the test says when it plays). the sources live on audiod's loop
// thread: reach them only through AudiodHarness::onLoop; the journal and the requests may be read from any thread
class FakeMedia {
 public:
  class Source;

  void spec(const std::string& name, FakeSourceSpec spec) {
    std::scoped_lock lock(mutex_);
    specs_[name] = spec;
  }

  [[nodiscard]] audiod::MediaSourceFactory factory() {
    return [this](const audiod::UrlRequest& request, std::shared_ptr<audiod::MediaSourceEvents> events)
               -> Result<std::unique_ptr<audiod::MediaSource>, std::string_view> {
      constexpr std::string_view prefix = "http://fake/";
      constexpr std::string_view refused = "http://fake/refused/";
      std::scoped_lock lock(mutex_);
      requests_.push_back(request);
      if (request.url.starts_with(refused)) {
        codes_.push_back(request.url.substr(refused.size()));
        return std::string_view(codes_.back());
      }
      if (!request.url.starts_with(prefix))
        return std::string_view("fake_url");
      const auto name = request.url.substr(prefix.size());
      journal_.push_back(name + ":made");
      auto source = std::make_unique<Source>(*this, name, specs_[name], std::move(events));
      sources_[name] = source.get();
      return std::unique_ptr<audiod::MediaSource>(std::move(source));
    };
  }

  // a long-lived source of that name, made when the sessions attach it
  [[nodiscard]] audiod::LongLivedSource longLived(const std::string& name, FakeSourceSpec spec) {
    return [this, name, spec](std::shared_ptr<audiod::MediaSourceEvents> events)
               -> std::unique_ptr<audiod::MediaSource> {
      std::scoped_lock lock(mutex_);
      journal_.push_back(name + ":made");
      auto source = std::make_unique<Source>(*this, name, spec, std::move(events));
      sources_[name] = source.get();
      return source;
    };
  }

  // loop thread: the live source of that name, or nullptr
  [[nodiscard]] Source* source(const std::string& name) {
    std::scoped_lock lock(mutex_);
    const auto found = sources_.find(name);
    return found == sources_.end() ? nullptr : found->second;
  }

  [[nodiscard]] std::vector<std::string> journal() const {
    std::scoped_lock lock(mutex_);
    return journal_;
  }

  // the calls of one source, without its name
  [[nodiscard]] std::vector<std::string> calls(const std::string& name) const {
    std::scoped_lock lock(mutex_);
    std::vector<std::string> calls;
    for (const auto& entry : journal_)
      if (entry.starts_with(name + ":"))
        calls.push_back(entry.substr(name.size() + 1));
    return calls;
  }

  [[nodiscard]] std::vector<audiod::UrlRequest> requests() const {
    std::scoped_lock lock(mutex_);
    return requests_;
  }

  class Source final : public audiod::MediaSource {
   public:
    Source(FakeMedia& media, std::string name, FakeSourceSpec spec, std::shared_ptr<audiod::MediaSourceEvents> events)
        : media_(media), name_(std::move(name)), spec_(spec), events_(std::move(events)) {}
    ~Source() override {
      quiet();
      media_.note(name_, "destroyed");
      std::scoped_lock lock(media_.mutex_);
      if (const auto found = media_.sources_.find(name_); found != media_.sources_.end() && found->second == this)
        media_.sources_.erase(found);
    }

    Source(const Source&) = delete;
    Source& operator=(const Source&) = delete;

    [[nodiscard]] audiod::MediaKind kind() const override { return spec_.kind; }
    [[nodiscard]] audiod::MediaActions actions() const override { return spec_.actions; }
    [[nodiscard]] bool keepsPaused() const override { return spec_.keepsPaused; }

    void openOutput(PcmInput& music) override {
      media_.note(name_, "open");
      music_ = &music;
      sound();
    }
    void closeOutput() override {
      media_.note(name_, "close");
      quiet();
      music_ = nullptr;
    }

    std::string_view pause() override {
      media_.note(name_, "pause");
      if (spec_.pauseFails)
        return audiod::kMediaFailed;
      quiet();
      events_->paused();
      return audiod::kMediaOk;
    }
    std::string_view resume() override {
      media_.note(name_, "resume");
      sound();
      return audiod::kMediaOk;
    }
    std::string_view stop() override {
      media_.note(name_, "stop");
      if (spec_.stopFails)
        return audiod::kMediaFailed;
      quiet();
      if (spec_.endsOnStop)
        events_->ended();
      return audiod::kMediaOk;
    }
    std::string_view seek(std::uint64_t ms) override {
      media_.note(name_, "seek " + std::to_string(ms));
      events_->position(ms);
      return audiod::kMediaOk;
    }
    std::string_view next() override {
      media_.note(name_, "next");
      return audiod::kMediaOk;
    }
    std::string_view previous() override {
      media_.note(name_, "previous");
      return audiod::kMediaOk;
    }
    std::string_view command(audiod::MediaAction action, std::uint64_t positionMs, audiod::CommandDone done) override {
      if (!spec_.answersLater || action == audiod::MediaAction::Stop || action == audiod::MediaAction::Seek)
        return MediaSource::command(action, positionMs, std::move(done));
      media_.note(name_, "later");
      later_.push_back(std::move(done));
      return audiod::kMediaPending;
    }
    // the oldest command waiting for its answer gets code
    void answer(std::string_view code) {
      REQUIRE_FALSE(later_.empty());
      auto done = std::move(later_.front());
      later_.pop_front();
      done(code);
    }

    // what the test makes it report
    [[nodiscard]] audiod::MediaSourceEvents& events() { return *events_; }
    // what its own threads would post
    [[nodiscard]] std::shared_ptr<audiod::MediaSourceEvents> handle() const { return events_; }
    // the input its output opened on, or nullptr
    [[nodiscard]] PcmInput* music() const { return music_; }

   private:
    // starts the tone thread, if the spec has one and the output is open
    void sound() {
      if (spec_.toneHz <= 0 || music_ == nullptr || player_.joinable())
        return;
      stopping_ = false;
      player_ = std::thread([this, input = music_, tone = sine(spec_.toneHz, 0.25, 960, 2)] {
        while (!stopping_)
          input->push(std::span<const float>(tone), 48000, 2);
      });
    }
    // stops pushing before it returns: a push waiting on a full input is let go by the clear
    void quiet() {
      if (!player_.joinable())
        return;
      stopping_ = true;
      music_->clear();
      player_.join();
    }

    FakeMedia& media_;
    std::string name_;
    FakeSourceSpec spec_;
    std::shared_ptr<audiod::MediaSourceEvents> events_;
    PcmInput* music_ = nullptr;
    std::deque<audiod::CommandDone> later_;
    std::atomic<bool> stopping_{false};
    std::thread player_;
  };

 private:
  void note(const std::string& name, const std::string& call) {
    std::scoped_lock lock(mutex_);
    journal_.push_back(name + ":" + call);
  }

  mutable std::mutex mutex_;
  std::map<std::string, FakeSourceSpec> specs_;
  std::map<std::string, Source*> sources_;
  std::vector<std::string> journal_;
  std::vector<audiod::UrlRequest> requests_;
  // a refused url's code, kept alive for the string_view the factory returns
  std::list<std::string> codes_;
};

}  // namespace cadence::test_support

#pragma once

#include "app/routes/core/router.h"
#include "app/ui-layer.h"
#include "ipc/codec.h"
#include "ui/images/image-cache.h"

#include <cstdint>
#include <functional>
#include <map>
#include <memory>
#include <string>
#include <vector>

// a fake server for i1's routes: answers the catalog's methods with made-up titles (no third-party text, no
// network), streams search and resolve on the ui's clock, and paints artwork itself. the gallery and the tests use it
namespace cadence::screen::routes::fake {

struct Script {
  // home: providers that answer unavailable (a strip), and ones the household has not configured
  std::vector<std::string> down = {"ytmusic"};
  std::vector<std::string> absent = {"spotify"};
  // home: a provider's answer in place of the made-up one (a test changes it over time)
  std::map<std::string, Json> home;
  // home and every read: never answer (the loading state)
  bool hang = false;
  // search: answers stream at these times after the call, and these sources never answer
  std::vector<int> searchTimesMs = {80, 200, 350, 500, 650, 800};
  bool searchHangs = false;
  bool searchEmpty = false;
  // the title remembers a voiceover (the picker is skipped), and has a continue point
  bool remembered = false;
  bool continuePoint = true;
  // episodes the title has
  int episodes = 24;
  // resolve and media.play: playable, no_seeders, or hang (nothing)
  std::string resolve = "playable";
  // the system layer over the routes, online, with devices.media answering the fake devices (home's strip)
  bool nearby = false;
};

struct Sent {
  std::string method;
  Json args;
  std::string id;
  bool subscribe = false;
};

class FakeCatalog final : public app::Outbox {
 public:
  explicit FakeCatalog(ui::Ui& ui, Script script = {});
  ~FakeCatalog() override;
  FakeCatalog(const FakeCatalog&) = delete;
  FakeCatalog& operator=(const FakeCatalog&) = delete;

  void setTarget(ui::Pages* target) { target_ = target; }
  bool send(const cadence_screen_v1_Envelope& e) override;
  // the plays media.play started, as cadenced's MediaPlay to the screen
  bool play(const cadence_screen_v1_Envelope& e);

  std::vector<Sent> calls;
  std::vector<std::string> cancels;
  std::vector<cadence_v2_ScreenContext> contexts;
  std::vector<std::string> contextQueries;
  struct Play {
    std::string sessionId;
    std::string url;
    std::string itemRef;
    std::string episode;
    std::uint64_t startMs = 0;
    std::size_t subtitles = 0;
    std::size_t skips = 0;
    // media.play's arguments
    Json args;
  };
  std::vector<Play> plays;
  Script script;

  // an event of an open call, now (the tests' own streams)
  void event(const std::string& id, const Json& json, bool last);
  void respond(const std::string& id, const std::string& status, const Json& json);
  [[nodiscard]] const Sent* last(const std::string& method) const;

 private:
  void answer(const Sent& s);
  void later(int ms, std::function<void()> fn);

  ui::Ui& ui_;
  ui::Pages* target_ = nullptr;
  std::uint32_t seq_ = 0;
  std::map<std::string, std::uint32_t> seqs_;
  std::vector<std::string> open_;
  // the ui's timers outlive a gallery page's session: an answer still due when it goes is dropped
  std::shared_ptr<bool> alive_ = std::make_shared<bool>(true);
};

// artwork painted from the id (a png, so the decode path is the device's)
class FakeImages final : public ui::images::Fetcher {
 public:
  bool fetch(const std::string& id, std::string& bytes, std::string& code) override;
  int fetched = 0;
};

// a 24-character image id for a seed
std::string imageId(int seed);

// the made-up catalogue
Json homeSections(const std::string& provider);
Json searchEvent(int step, bool empty);
Json titleDetail(const Script& s, const std::string& ref);
Json titleOptions(const Script& s, const std::string& episode);
Json playable(const std::string& ref, const std::string& episode);
// cadenced's MediaPlay for the server's play of a playable
bool playEnvelope(const Json& playable, const std::string& sessionId, ipc::OutEnvelope& out);

}  // namespace cadence::screen::routes::fake

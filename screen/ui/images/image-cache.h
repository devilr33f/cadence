#pragma once

#include "ui/rml/sprites.h"

#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::screen::ui::images {

// how an image's bytes arrive: the server's image proxy (GET /v2/images/{id}) on the device, a fake in tests. called
// on a worker thread, so it must be thread-safe. an id is a capability (it alone opens the image): never logged
class Fetcher {
 public:
  virtual ~Fetcher() = default;
  // the encoded image (png, jpeg), or false with a token in code (not_found, unavailable, too_large, ...)
  virtual bool fetch(const std::string& id, std::string& bytes, std::string& code) = 0;
};

// an image id as the catalog and cards hand them out (the proxy's own pattern)
bool validId(std::string_view id);

struct CacheOptions {
  // decoded pixels kept, on the cpu until uploaded and on the gpu after (design r2 section 11: 48 MB on home)
  std::size_t budgetBytes = 48u << 20;
  int workers = 2;
  // fetch and decode on the calling thread at once (the gallery and tests: deterministic frames)
  bool inlineDecode = false;
  // a request asks for at most this many pixels a side (section 25: a backdrop decodes at 960 x 540 and scales up)
  int maxSide = 1920;
  std::size_t maxEncodedBytes = 4u << 20;
  // queued requests over this drop the oldest (the newest are what focus just reached)
  std::size_t maxQueued = 48;
};

enum class State { Missing, Pending, Ready, Resident, Failed };

// decoded images by id and display size, decoded off the ui thread (never at full size: scaled to cover the slot),
// evicted least recently used within the budget. the renderer takes a ready image's pixels once (they go to the
// gpu, and the cpu copy is dropped); the binder pins what a document shows, so a shown image is never evicted.
// request, poll, take and pin run on the ui thread; workers only fetch and decode
class ImageCache {
 public:
  ImageCache(std::shared_ptr<Fetcher> fetcher, CacheOptions options = {});
  ~ImageCache();
  ImageCache(const ImageCache&) = delete;
  ImageCache& operator=(const ImageCache&) = delete;

  static std::string keyOf(std::string_view id, int width, int height, int blur = 0, int radius = 0);

  // queues the image at that size unless it is held; its state now, and the key it is held under (sizes snap to 8 px).
  // blur > 0 softens it by that many pixels once decoded (a backdrop's, images/blur.h); radius > 0 decodes it at
  // exactly its slot's shape with its corners rounded by that many pixels (its width still snaps, its height
  // follows), so it draws rounded without a clip mask
  State request(const std::string& id, int width, int height, std::string* key = nullptr, int blur = 0, int radius = 0);
  [[nodiscard]] State state(const std::string& key) const;
  // a ready image's pixels still to upload, in bytes (0 for any other state)
  [[nodiscard]] std::size_t readyBytes(const std::string& key) const;
  // keys that became ready or failed since the last poll; runs the evictions the budget needs
  std::vector<std::string> poll();
  // a ready image's pixels for the gpu (the cpu copy goes; the entry stays resident); false when it holds none
  bool take(const std::string& key, Pixels& out);
  // a resident image's texture is gone (a lost context): it is fetched again on its next request
  void lost(const std::string& key);
  void pin(const std::string& key);
  void unpin(const std::string& key);
  // called on the ui thread for each evicted resident key (the renderer's texture goes with it)
  void onEvict(std::function<void(const std::string& key)> fn) { evict_ = std::move(fn); }
  // a binder (a ui) uses the cache
  [[nodiscard]] bool bound() const { return static_cast<bool>(evict_); }
  // a failed key may be asked again (retry after the server came back)
  void forgetFailures();
  // id's bytes come from a local file (a cover cadenced wrote for the screen), not the fetcher; the latest few are
  // kept. a path is a device file: never logged
  void addLocal(const std::string& id, const std::string& path);

  struct Stats {
    std::size_t entries = 0;
    std::size_t bytes = 0;
    std::size_t peakBytes = 0;
    std::size_t decoded = 0;
    std::size_t failed = 0;
    std::size_t evicted = 0;
    std::size_t dropped = 0;
    std::size_t hits = 0;
  };
  [[nodiscard]] Stats stats() const;
  [[nodiscard]] std::size_t budget() const { return options_.budgetBytes; }

 private:
  struct Entry {
    State state = State::Missing;
    std::string id;
    int width = 0;
    int height = 0;
    Pixels pixels;
    std::size_t bytes = 0;
    std::uint64_t used = 0;
    int pins = 0;
  };
  struct Job {
    std::string key;
    std::string id;
    int width;
    int height;
    int blur = 0;
    int radius = 0;
  };
  struct Done {
    std::string key;
    bool ok;
    Pixels pixels;
  };

  void work();
  Done run(const Job& job);
  // a fetch or decode failure's line (codes and sizes alone: an id is a capability), each event and code at most
  // once a minute, with how many it held back
  void logFailure(const char* event, const std::string& code, std::size_t bytes);
  void finish(Done done);
  void enforceBudget();

  std::shared_ptr<Fetcher> fetcher_;
  CacheOptions options_;
  std::map<std::string, Entry> entries_;
  std::uint64_t clock_ = 0;
  std::vector<std::string> fresh_;
  std::function<void(const std::string&)> evict_;
  Stats stats_;

  mutable std::mutex mu_;
  std::condition_variable cv_;
  std::deque<Job> queue_;
  // local files by id (addLocal), oldest first; under mu_
  std::deque<std::pair<std::string, std::string>> local_;
  std::vector<Done> done_;
  bool stop_ = false;
  std::vector<std::thread> threads_;

  std::mutex logMu_;
  struct Logged {
    std::chrono::steady_clock::time_point at;
    std::size_t held = 0;
  };
  std::map<std::string, Logged> logged_;
};

// what an encoded image's first bytes say it is (png, jpeg, webp, gif, avif, bmp, unknown): a decode failure's code
std::string formatOf(const std::string& bytes);

// decodes png or jpeg and scales it (box filter) to cover width x height without upscaling; premultiplied rgba.
// false for anything else, or over 4096 a side
bool decodeToCover(const std::string& bytes, int width, int height, Pixels& out);

}  // namespace cadence::screen::ui::images

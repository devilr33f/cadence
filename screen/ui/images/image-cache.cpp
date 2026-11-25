#include "ui/images/image-cache.h"

#include "ui/images/blur.h"

#include "ipc/log.h"

#if defined(__GNUC__)
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wconversion"
#pragma GCC diagnostic ignored "-Wsign-conversion"
#pragma GCC diagnostic ignored "-Wshadow"
#endif
// the implementation is the renderer's (renderer.cpp): png and jpeg only
#define STBI_NO_STDIO
#include <stb/stb_image.h>
#if defined(__GNUC__)
#pragma GCC diagnostic pop
#endif

#include <algorithm>
#include <fstream>
#include <iterator>
#include <cmath>

namespace cadence::screen::ui::images {

namespace {

constexpr int kMaxSourceSide = 4096;

}  // namespace

bool validId(std::string_view id) {
  if (id.size() != 24) return false;
  return std::all_of(id.begin(), id.end(), [](char c) {
    return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-';
  });
}

std::string formatOf(const std::string& bytes) {
  const std::string_view b(bytes);
  if (b.starts_with("\x89PNG")) return "png";
  if (b.starts_with("\xff\xd8\xff")) return "jpeg";
  if (b.starts_with("GIF8")) return "gif";
  if (b.starts_with("BM")) return "bmp";
  if (b.size() >= 12 && b.starts_with("RIFF") && b.substr(8, 4) == "WEBP") return "webp";
  if (b.size() >= 12 && b.substr(4, 4) == "ftyp" && (b.substr(8, 4) == "avif" || b.substr(8, 4) == "avis")) return "avif";
  return "unknown";
}

bool decodeToCover(const std::string& bytes, int width, int height, Pixels& out) {
  if (bytes.empty() || bytes.size() > static_cast<std::size_t>(INT32_MAX)) return false;
  const auto* data = reinterpret_cast<const stbi_uc*>(bytes.data());
  const int len = static_cast<int>(bytes.size());
  int w = 0, h = 0, n = 0;
  // the header first: a huge image is refused before anything is allocated for it
  if (stbi_info_from_memory(data, len, &w, &h, &n) == 0 || w <= 0 || h <= 0 || w > kMaxSourceSide || h > kMaxSourceSide) {
    return false;
  }
  stbi_uc* src = stbi_load_from_memory(data, len, &w, &h, &n, 4);
  if (src == nullptr) return false;
  width = std::max(1, width);
  height = std::max(1, height);
  // cover the slot: the larger of the two ratios, never above 1 (the decorator scales up what is smaller)
  const double s = std::min(1.0, std::max(static_cast<double>(width) / w, static_cast<double>(height) / h));
  const int ow = std::max(1, static_cast<int>(std::ceil(w * s)));
  const int oh = std::max(1, static_cast<int>(std::ceil(h * s)));
  out.width = ow;
  out.height = oh;
  out.rgba.assign(static_cast<std::size_t>(ow) * static_cast<std::size_t>(oh) * 4, 0);
  const auto at = [&](int x, int y) { return src + (static_cast<std::size_t>(y) * static_cast<std::size_t>(w) + static_cast<std::size_t>(x)) * 4; };
  for (int y = 0; y < oh; ++y) {
    const int y0 = y * h / oh;
    const int y1 = std::max(y0 + 1, (y + 1) * h / oh);
    for (int x = 0; x < ow; ++x) {
      const int x0 = x * w / ow;
      const int x1 = std::max(x0 + 1, (x + 1) * w / ow);
      // a box filter over the source pixels this one covers, premultiplied as it sums
      std::uint64_t r = 0, g = 0, b = 0, a = 0, count = 0;
      for (int yy = y0; yy < y1; ++yy) {
        for (int xx = x0; xx < x1; ++xx) {
          const auto* p = at(xx, yy);
          const std::uint64_t pa = p[3];
          r += p[0] * pa;
          g += p[1] * pa;
          b += p[2] * pa;
          a += pa;
          ++count;
        }
      }
      auto* d = out.rgba.data() + (static_cast<std::size_t>(y) * static_cast<std::size_t>(ow) + static_cast<std::size_t>(x)) * 4;
      const std::uint64_t div = count * 255;
      d[0] = static_cast<std::uint8_t>((r + div / 2) / div);
      d[1] = static_cast<std::uint8_t>((g + div / 2) / div);
      d[2] = static_cast<std::uint8_t>((b + div / 2) / div);
      d[3] = static_cast<std::uint8_t>((a + count / 2) / count);
    }
  }
  stbi_image_free(src);
  return true;
}

ImageCache::ImageCache(std::shared_ptr<Fetcher> fetcher, CacheOptions options)
    : fetcher_(std::move(fetcher)), options_(options) {
  if (!options_.inlineDecode) {
    for (int i = 0; i < std::max(1, options_.workers); ++i) threads_.emplace_back([this] { work(); });
  }
}

ImageCache::~ImageCache() {
  {
    std::lock_guard lock(mu_);
    stop_ = true;
    queue_.clear();
  }
  cv_.notify_all();
  for (auto& t : threads_) t.join();
}

std::string ImageCache::keyOf(std::string_view id, int width, int height, int blur, int radius) {
  return std::string(id) + "@" + std::to_string(width) + "x" + std::to_string(height) +
         (blur > 0 ? "~" + std::to_string(blur) : std::string()) + (radius > 0 ? "r" + std::to_string(radius) : std::string());
}

State ImageCache::request(const std::string& id, int width, int height, std::string* keyOut, int blur, int radius) {
  if (!validId(id) || fetcher_ == nullptr) return State::Failed;
  blur = std::clamp(blur, 0, 64);
  radius = std::clamp(radius, 0, 256);
  if (radius > 0 && blur == 0 && width > 0 && height > 0) {
    // rounded: the slot's own shape, so its decorator never crops a corner away; the width snaps, the height follows
    const int w = std::clamp((width + 7) / 8 * 8, 8, options_.maxSide);
    height = std::clamp(static_cast<int>(std::lround(static_cast<double>(height) * w / width)), 8, options_.maxSide);
    radius = static_cast<int>(std::lround(static_cast<double>(radius) * w / width));
    width = w;
  } else {
    // slots snap to 8 px, so a card that scales on focus never asks for a second copy
    width = std::clamp((width + 7) / 8 * 8, 8, options_.maxSide);
    height = std::clamp((height + 7) / 8 * 8, 8, options_.maxSide);
    radius = 0;
  }
  const auto key = keyOf(id, width, height, blur, radius);
  if (keyOut != nullptr) *keyOut = key;
  auto& e = entries_[key];
  e.used = ++clock_;
  if (e.state != State::Missing) {
    if (e.state == State::Ready || e.state == State::Resident) stats_.hits++;
    return e.state;
  }
  e.state = State::Pending;
  e.id = id;
  e.width = width;
  e.height = height;
  Job job{key, id, width, height, blur, radius};
  if (options_.inlineDecode) {
    finish(run(job));
    return entries_[key].state;
  }
  {
    std::lock_guard lock(mu_);
    queue_.push_back(std::move(job));
    while (queue_.size() > options_.maxQueued) {
      // the oldest request is dropped: it goes back to missing, so its element asks again if it is still shown
      const auto dropped = queue_.front().key;
      queue_.pop_front();
      fresh_.push_back(dropped);
      stats_.dropped++;
      // its entry is reset when poll() runs on this thread, below
      entries_[dropped].state = State::Missing;
    }
  }
  cv_.notify_one();
  return State::Pending;
}

State ImageCache::state(const std::string& key) const {
  const auto it = entries_.find(key);
  return it == entries_.end() ? State::Missing : it->second.state;
}

std::size_t ImageCache::readyBytes(const std::string& key) const {
  const auto it = entries_.find(key);
  return it == entries_.end() || it->second.state != State::Ready ? 0 : it->second.pixels.rgba.size();
}

void ImageCache::work() {
  for (;;) {
    Job job;
    {
      std::unique_lock lock(mu_);
      cv_.wait(lock, [this] { return stop_ || !queue_.empty(); });
      if (stop_) return;
      // newest first: what focus reached last is what the viewer looks at
      job = std::move(queue_.back());
      queue_.pop_back();
    }
    auto done = run(job);
    std::lock_guard lock(mu_);
    if (stop_) return;
    done_.push_back(std::move(done));
  }
}

void ImageCache::addLocal(const std::string& id, const std::string& path) {
  if (!validId(id) || path.empty()) return;
  std::lock_guard lock(mu_);
  std::erase_if(local_, [&](const auto& l) { return l.first == id; });
  local_.emplace_back(id, path);
  while (local_.size() > 4) local_.pop_front();
}

namespace {

bool readLocal(const std::string& path, std::size_t maxBytes, std::string& bytes, std::string& code) {
  std::ifstream in(path, std::ios::binary);
  if (!in) {
    code = "not_found";
    return false;
  }
  bytes.assign(std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>());
  if (bytes.size() > maxBytes) {
    code = "too_large";
    return false;
  }
  return true;
}

}  // namespace

ImageCache::Done ImageCache::run(const Job& job) {
  Done d{job.key, false, {}};
  std::string bytes;
  std::string code;
  std::string localPath;
  {
    std::lock_guard lock(mu_);
    for (const auto& [id, path] : local_)
      if (id == job.id) localPath = path;
  }
  const bool fetched = !localPath.empty() ? readLocal(localPath, options_.maxEncodedBytes, bytes, code)
                                          : fetcher_->fetch(job.id, bytes, code);
  if (!fetched) {
    logFailure("image_fetch_failed", code.empty() ? "failed" : code, 0);
    return d;
  }
  if (bytes.size() > options_.maxEncodedBytes) {
    logFailure("image_fetch_failed", "too_large", bytes.size());
    return d;
  }
  d.ok = decodeToCover(bytes, job.width, job.height, d.pixels);
  if (d.ok && job.blur > 0) {
    // a backdrop: at exactly its slot's size, then softened (images/blur.h)
    fitCover(d.pixels, job.width, job.height);
    blur(d.pixels, job.blur);
  }
  if (d.ok && job.radius > 0) {
    // a rounded slot: at exactly its shape, its corners cleared once here rather than masked each frame
    fitCover(d.pixels, job.width, job.height);
    roundCorners(d.pixels, job.radius);
  }
  if (!d.ok) logFailure("image_decode_failed", formatOf(bytes), bytes.size());
  return d;
}

void ImageCache::logFailure(const char* event, const std::string& code, std::size_t bytes) {
  const auto now = std::chrono::steady_clock::now();
  std::size_t held = 0;
  {
    std::lock_guard lock(logMu_);
    auto [it, fresh] = logged_.try_emplace(std::string(event) + ":" + code);
    if (!fresh && now - it->second.at < std::chrono::minutes(1)) {
      it->second.held++;
      return;
    }
    held = it->second.held;
    it->second = {now, 0};
  }
  CADENCE_LOG(Warn, event, {{"code", ipc::Token(code)}, {"bytes", static_cast<std::uint64_t>(bytes)}, {"held", static_cast<std::uint64_t>(held)}});
}

void ImageCache::finish(Done done) {
  const auto it = entries_.find(done.key);
  if (it == entries_.end() || it->second.state != State::Pending) return;
  auto& e = it->second;
  if (done.ok) {
    e.state = State::Ready;
    e.bytes = done.pixels.rgba.size();
    e.pixels = std::move(done.pixels);
    stats_.decoded++;
    stats_.bytes += e.bytes;
    stats_.peakBytes = std::max(stats_.peakBytes, stats_.bytes);
  } else {
    e.state = State::Failed;
    stats_.failed++;
  }
  fresh_.push_back(done.key);
  enforceBudget();
}

std::vector<std::string> ImageCache::poll() {
  std::vector<Done> done;
  {
    std::lock_guard lock(mu_);
    done.swap(done_);
  }
  for (auto& d : done) finish(std::move(d));
  std::vector<std::string> out;
  out.swap(fresh_);
  // a dropped request leaves no entry behind
  for (auto it = entries_.begin(); it != entries_.end();) {
    if (it->second.state == State::Missing && it->second.pins == 0) {
      it = entries_.erase(it);
    } else {
      ++it;
    }
  }
  return out;
}

bool ImageCache::take(const std::string& key, Pixels& out) {
  const auto it = entries_.find(key);
  if (it == entries_.end() || it->second.state != State::Ready) return false;
  auto& e = it->second;
  out = std::move(e.pixels);
  e.pixels = {};
  // the bytes now live on the gpu: still counted
  e.state = State::Resident;
  e.used = ++clock_;
  return true;
}

void ImageCache::lost(const std::string& key) {
  const auto it = entries_.find(key);
  if (it == entries_.end() || it->second.state != State::Resident) return;
  stats_.bytes -= it->second.bytes;
  it->second.bytes = 0;
  it->second.state = State::Missing;
}

void ImageCache::pin(const std::string& key) {
  auto& e = entries_[key];
  e.pins++;
  e.used = ++clock_;
}

void ImageCache::unpin(const std::string& key) {
  const auto it = entries_.find(key);
  if (it == entries_.end()) return;
  it->second.pins = std::max(0, it->second.pins - 1);
  enforceBudget();
}

void ImageCache::forgetFailures() {
  for (auto it = entries_.begin(); it != entries_.end();) {
    if (it->second.state == State::Failed && it->second.pins == 0) {
      it = entries_.erase(it);
    } else {
      ++it;
    }
  }
}

void ImageCache::enforceBudget() {
  while (stats_.bytes > options_.budgetBytes) {
    // the least recently used image nobody shows
    auto victim = entries_.end();
    for (auto it = entries_.begin(); it != entries_.end(); ++it) {
      const auto& e = it->second;
      if (e.pins > 0 || e.bytes == 0) continue;
      if (victim == entries_.end() || e.used < victim->second.used) victim = it;
    }
    if (victim == entries_.end()) return;
    const bool resident = victim->second.state == State::Resident;
    stats_.bytes -= victim->second.bytes;
    stats_.evicted++;
    const auto key = victim->first;
    entries_.erase(victim);
    if (resident && evict_) evict_(key);
  }
}

ImageCache::Stats ImageCache::stats() const {
  auto s = stats_;
  s.entries = entries_.size();
  return s;
}

}  // namespace cadence::screen::ui::images

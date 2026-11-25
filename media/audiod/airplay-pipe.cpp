#include "audiod/airplay-pipe.h"

#include "audiod/airplay-files.h"

#include <fcntl.h>
#include <poll.h>
#include <sys/eventfd.h>
#include <sys/stat.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <chrono>
#include <cstring>
#include <string_view>
#include <utility>
#include <vector>

namespace cadence::audiod {

namespace {

constexpr std::size_t kFrameBytes = kAirplayChannels * sizeof(std::int16_t);
// what one read takes: about 85 ms of audio
constexpr std::size_t kReadBytes = 16 * 1024;
// reads per wake before the stop fd is looked at again, so a writer faster than real time cannot hold the thread
constexpr int kReadsPerWake = 16;
// a fifo that would not open is tried again this often
constexpr int kRetryMs = 100;

}  // namespace

void AirplayOutput::open(PcmInput& music) {
  std::unique_lock lock(mutex_);
  // a stop waits with the lock let go: an open from another thread may come in meanwhile
  while (open_ && music_ != &music)
    set(lock, &AirplayOutput::open_, false);
  if (open_)
    return;
  music_ = &music;
  set(lock, &AirplayOutput::open_, true);
}

// the flow stops first, waiting out a push under way, so nothing holds the input once it is let go
void AirplayOutput::close() {
  std::unique_lock lock(mutex_);
  set(lock, &AirplayOutput::open_, false);
  // an open from another thread may have come in while the stop waited
  if (!open_)
    music_ = nullptr;
}

void AirplayOutput::gate(bool open) { set(&AirplayOutput::gate_, open); }

void AirplayOutput::hold(bool held) { set(&AirplayOutput::held_, held); }

std::uint64_t AirplayOutput::pause() {
  std::unique_lock lock(mutex_);
  const auto number = ++pauses_;
  set(lock, &AirplayOutput::held_, true);
  return number;
}

// under the same lock as the pause, so a release that checked before it cannot land after it
void AirplayOutput::release(std::uint64_t pausesTaken) {
  std::unique_lock lock(mutex_);
  if (pausesTaken < pauses_)
    return;
  set(lock, &AirplayOutput::held_, false);
}

std::uint64_t AirplayOutput::pauses() const {
  const std::scoped_lock lock(mutex_);
  return pauses_;
}

// each caller changes its own flag under the lock, so two from different threads never undo each other
void AirplayOutput::set(bool AirplayOutput::*flag, bool value) {
  std::unique_lock lock(mutex_);
  set(lock, flag, value);
}

void AirplayOutput::set(std::unique_lock<std::mutex>& lock, bool AirplayOutput::*flag, bool value) {
  const bool was = flowing();
  this->*flag = value;
  if (was == flowing())
    return;
  // nothing queued before survives either edge
  if (flowing()) {
    ++clears_;
    music_->clear();
    music_->attach();
    opened_ = music_->status();
    return;
  }
  // how full the input was as the flow stopped, before the clear empties it. the wait below lets go of the lock, so
  // the input is this flow's own from here
  auto& music = *music_;
  const auto queued = music.status().queued;
  music.detach();
  ++clears_;
  music.clear();
  changed_.wait(lock, [this] { return !pushing_; });
  // after the push under way, which counts its frames too
  const auto closed = music.status();
  log_.info("airplay_output_closed", {{"frames", closed.framesIn - opened_.framesIn},
                                      {"dropped", closed.dropped - opened_.dropped},
                                      {"underruns", closed.underruns - opened_.underruns},
                                      {"queued_ms", queued * 1000 / kAirplayRate}});
}

void AirplayOutput::deliver(std::span<const std::int16_t> samples) {
  std::unique_lock lock(mutex_);
  if (!flowing())
    return;
  const auto clears = clears_;
  // a stop waits for this push before close lets go of the input, so it stays valid unlocked
  auto& music = *music_;
  pushing_ = true;
  lock.unlock();
  music.tryPush(samples, kAirplayRate, kAirplayChannels);
  lock.lock();
  // a stop landed as the push began, which then queued after its clear: it goes too
  if (clears_ != clears)
    music.clear();
  pushing_ = false;
  changed_.notify_all();
}

struct AirplayPipe::Fifo {
  // audio|metadata, for logs
  std::string_view name;
  const std::string& path;
  UniqueFd fd;
  // what this session's writer wrote
  std::uint64_t bytes = 0;
  // the start of a frame whose rest has not come yet
  std::vector<std::uint8_t> partial;
  // its open failed and was logged; logged again only once it opened
  bool failing = false;
};

std::unique_ptr<AirplayPipe> AirplayPipe::start(const Log& log, std::string audio, std::string metadata,
                                                AirplayOutput& output, MetadataSink sink, ProgressSink progress,
                                                std::string artworkPath) {
  UniqueFd stop(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK));
  if (!stop) {
    log.error("airplay_pipe_failed", {{"call", "eventfd"}, {"errno", errno}});
    return nullptr;
  }
  std::unique_ptr<AirplayPipe> pipe(
      new AirplayPipe(log, std::move(audio), std::move(metadata), output, std::move(sink), std::move(progress),
                      std::move(artworkPath), std::move(stop)));
  try {
    pipe->thread_ = std::thread([raw = pipe.get()] { raw->run(); });
  } catch (...) {
    log.error("airplay_pipe_failed", {{"call", "thread"}, {"errno", 0}});
    return nullptr;
  }
  return pipe;
}

AirplayPipe::AirplayPipe(const Log& log, std::string audio, std::string metadata, AirplayOutput& output,
                         MetadataSink sink, ProgressSink progress, std::string artworkPath, UniqueFd stop)
    : log_(log),
      audioPath_(std::move(audio)),
      metadataPath_(std::move(metadata)),
      output_(output),
      sink_(std::move(sink)),
      progress_(std::move(progress)),
      artworkPath_(std::move(artworkPath)),
      stop_(std::move(stop)) {}

void AirplayPipe::publish(MediaMetadata metadata) {
  if (!metadata.artworkId.empty() && metadata.artworkId != written_) {
    const auto temporary = artworkPath_ + ".new";
    std::optional<SysError> error;
    if (artworkPath_.empty())
      error = SysError{"path", EINVAL};
    else if ((error = airplay::writePrivate(temporary, tracker_.artwork())); !error &&
             ::rename(temporary.c_str(), artworkPath_.c_str()) != 0)
      error = SysError{"rename", errno};
    if (error) {
      // the session shows no cover rather than another track's
      if (!artworkPath_.empty())
        log_.warn("airplay_artwork_failed", {{"call", error->call}, {"errno", error->code}});
      metadata.artworkId.clear();
      written_.clear();
    } else {
      written_ = metadata.artworkId;
      log_.info("airplay_artwork", {{"bytes", tracker_.artwork().size()}});
    }
  }
  if (sink_)
    sink_(metadata);
}

AirplayPipe::~AirplayPipe() {
  const std::uint64_t one = 1;
  [[maybe_unused]] const auto written = ::write(stop_.get(), &one, sizeof one);
  if (thread_.joinable())
    thread_.join();
}

void AirplayPipe::run() {
  Fifo audio{.name = "audio", .path = audioPath_, .fd = {}, .bytes = 0, .partial = {}, .failing = false};
  Fifo metadata{.name = "metadata", .path = metadataPath_, .fd = {}, .bytes = 0, .partial = {}, .failing = false};
  // the read end opens at once, writer or not; a symlink or anything but a fifo is refused
  const auto open = [this](Fifo& fifo) {
    fifo.fd = UniqueFd(::open(fifo.path.c_str(), O_RDONLY | O_NONBLOCK | O_NOFOLLOW | O_CLOEXEC));
    int code = errno;
    if (fifo.fd) {
      struct stat info {};
      if (::fstat(fifo.fd.get(), &info) != 0)
        code = errno;
      else if (!S_ISFIFO(info.st_mode))
        code = EINVAL;
      else
        code = 0;
      if (code != 0)
        fifo.fd.reset();
    }
    if (!fifo.fd && !fifo.failing)
      log_.warn("airplay_pipe_open_failed", {{"fifo", fifo.name}, {"errno", code}});
    fifo.failing = !fifo.fd;
  };
  // its writer closed it: the next session opens it again
  const auto reopen = [&](Fifo& fifo) {
    if (&fifo == &metadata) {
      // what the reader skipped is counted, never shown
      log_.info("airplay_pipe_closed", {{"fifo", fifo.name}, {"bytes", fifo.bytes}, {"skipped", reader_.skipped()}});
      // half an item or half a sequence of the writer that went never joins the next one's
      reader_.reset();
      if (const auto cleared = tracker_.reset(); cleared)
        publish(*cleared);
    } else {
      log_.info("airplay_pipe_closed", {{"fifo", fifo.name}, {"bytes", fifo.bytes}});
    }
    fifo.bytes = 0;
    fifo.partial.clear();
    open(fifo);
  };
  open(audio);
  open(metadata);
  bool pollFailing = false;
  for (;;) {
    std::array<pollfd, 3> fds{pollfd{stop_.get(), POLLIN, 0}, pollfd{audio.fd.get(), POLLIN, 0},
                              pollfd{metadata.fd.get(), POLLIN, 0}};
    const bool retry = !audio.fd || !metadata.fd;
    if (::poll(fds.data(), fds.size(), retry ? kRetryMs : -1) < 0 && errno != EINTR) {
      // shairport must find a reader whatever happens here: the fifos stay open, and the poll is tried again
      if (!pollFailing)
        log_.error("airplay_pipe_failed", {{"call", "poll"}, {"errno", errno}});
      pollFailing = true;
      std::this_thread::sleep_for(std::chrono::milliseconds(kRetryMs));
      continue;
    }
    pollFailing = false;
    if (fds[0].revents != 0)
      return;
    if (fds[1].revents != 0 && !readAudio(audio))
      reopen(audio);
    if (fds[2].revents != 0 && !readMetadata(metadata))
      reopen(metadata);
    for (auto* fifo : {&audio, &metadata})
      if (!fifo->fd)
        open(*fifo);
  }
}

bool AirplayPipe::readAudio(Fifo& fifo) {
  std::array<std::uint8_t, kReadBytes> chunk{};
  std::vector<std::int16_t> samples;
  for (int reads = 0; reads < kReadsPerWake; ++reads) {
    const auto got = ::read(fifo.fd.get(), chunk.data(), chunk.size());
    if (got < 0 && errno == EINTR)
      continue;
    if (got < 0 && errno == EAGAIN)
      return true;
    if (got <= 0)
      return false;
    const auto size = static_cast<std::size_t>(got);
    fifo.bytes += size;
    fifo.partial.insert(fifo.partial.end(), chunk.begin(), chunk.begin() + static_cast<std::ptrdiff_t>(size));
    const auto whole = fifo.partial.size() / kFrameBytes * kFrameBytes;
    if (whole == 0)
      continue;
    // the bytes need not be aligned for their samples
    samples.resize(whole / sizeof(std::int16_t));
    std::memcpy(samples.data(), fifo.partial.data(), whole);
    fifo.partial.erase(fifo.partial.begin(), fifo.partial.begin() + static_cast<std::ptrdiff_t>(whole));
    output_.deliver(samples);
  }
  return true;
}

bool AirplayPipe::readMetadata(Fifo& fifo) {
  std::array<std::uint8_t, kReadBytes> chunk{};
  std::vector<airplay::MetadataItem> items;
  for (int reads = 0; reads < kReadsPerWake; ++reads) {
    const auto got = ::read(fifo.fd.get(), chunk.data(), chunk.size());
    if (got < 0 && errno == EINTR)
      continue;
    if (got < 0 && errno == EAGAIN)
      return true;
    if (got <= 0)
      return false;
    const auto size = static_cast<std::size_t>(got);
    fifo.bytes += size;
    items.clear();
    reader_.feed(std::span(chunk).first(size), items);
    for (const auto& item : items) {
      if (const auto changed = tracker_.item(item); changed)
        publish(*changed);
      else if (const auto where = airplay::progress(item); where && progress_)
        progress_(*where);
    }
  }
  return true;
}

}  // namespace cadence::audiod

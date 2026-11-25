#include "support/fake-aaudio.h"

#include <utility>

namespace cadence::fake_aaudio {

Result<std::unique_ptr<aaudio::Stream>, std::string> FakeApi::open(const aaudio::StreamRequest& request,
                                                                   aaudio::DataCallback data,
                                                                   aaudio::ErrorCallback error) {
  requests.push_back(request);
  if (openError)
    return *openError;
  auto s = std::make_unique<FakeStream>();
  s->api = this;
  s->request = request;
  s->granted = {grantRate.value_or(request.sampleRate), grantChannels.value_or(request.channels), burst, 2 * burst,
                request.lowLatency};
  s->data = std::move(data);
  s->error = std::move(error);
  ++opens;
  live_ = s.get();
  return std::unique_ptr<aaudio::Stream>(std::move(s));
}

FakeApi::FakeStream::~FakeStream() {
  if (api != nullptr && api->live_ == this)
    api->live_ = nullptr;
}

bool FakeApi::FakeStream::start() {
  if (api->startFails)
    return false;
  started = true;
  return true;
}

std::vector<std::int16_t> FakeApi::pull(std::size_t frames) {
  std::vector<std::int16_t> out(frames * static_cast<std::size_t>(live_->granted.channels), 7);
  live_->data(out);
  live_->written += static_cast<std::int64_t>(frames);
  return out;
}

void FakeApi::push(std::span<const std::int16_t> interleaved) {
  std::vector<std::int16_t> in(interleaved.begin(), interleaved.end());
  live_->data(in);
  live_->read += static_cast<std::int64_t>(in.size() / static_cast<std::size_t>(live_->granted.channels));
}

void FakeApi::disconnect() { live_->error(-899); }

}  // namespace cadence::fake_aaudio

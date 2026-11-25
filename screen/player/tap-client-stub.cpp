// the tap's client, stubbed for macos (the screen-sim build): the tap is audiod's, over a SOCK_SEQPACKET socket with
// scm_rights rings, and no macos build ever runs beside an audiod. the sim's fake cadenced sends no tap path, so this
// stub only has to hold the app's sinks still
#include "player/tap-client.h"

namespace cadence::screen::player {

// the tap client's members name a vector<RingOffer>, so the ring's special members must exist even here, where no
// ring is ever made: defaulted (fds start closed, pointers null), and never run
PcmRing::PcmRing(PcmRing&&) noexcept = default;
PcmRing& PcmRing::operator=(PcmRing&&) noexcept = default;
PcmRing::~PcmRing() = default;

TapClient::TapClient(TapSinks sinks) { (void)sinks; }
TapClient::~TapClient() = default;

void TapClient::connect(const std::string& path) { (void)path; }
void TapClient::adopt(int fd) { (void)fd; }
void TapClient::stop() {}

std::vector<RingOffer> TapClient::takeRings() { return {}; }
TapClient::Counts TapClient::counts() const { return {}; }

}  // namespace cadence::screen::player

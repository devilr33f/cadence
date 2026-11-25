#pragma once

#include <sys/types.h>

#include <cstdint>
#include <optional>
#include <string>
#include <string_view>

// what the airplay daemons are configured with (reference §2.2, §2.3), each file written afresh per attempt
namespace cadence::audiod::airplay {

// v1's receiver id: the address of the first interface under interfaces, by name, that its flags do not mark
// loopback and whose address reads exactly xx:xx:xx:xx:xx:xx and is not zero. shairport's airplay_device_id and
// avahi's host name
std::optional<std::uint64_t> receiverId(const std::string& interfaces);
std::optional<std::uint64_t> parseAddress(std::string_view text);

// a value inside a libconfig string, as v1 wrote it: \ and " escaped, and ${ spelled $${, since shairport substitutes
// ${NAME} from its environment before it parses; nullopt for a control character
std::optional<std::string> quote(std::string_view value);

// the private bus (v1 §2.2): a system bus listening on socket alone, EXTERNAL only, open to uid alone
std::string busConfig(const std::string& socket, uid_t uid);
std::string avahiConfig(std::uint64_t receiverId);
// shairport (v1 §2.3, M4d-R2): the pipe backend into audioFifo at 48 kHz s16 stereo, the metadata into metadataFifo
// without cover art or a cache for it, its d-bus interface on the private bus as the system one, unity gain; nullopt
// when the name or a path cannot be quoted
std::optional<std::string> shairportConfig(std::string_view name, std::uint64_t receiverId,
                                           const std::string& audioFifo, const std::string& metadataFifo);

}  // namespace cadence::audiod::airplay

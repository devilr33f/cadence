#pragma once

#include <string_view>

// what home does with a provider's browse answer (pure: the unit tests pin it). a feed that does not exist for this
// household is no row, silently: a provider without browse (ytmusic answers unsupported), one not configured or a
// feed its instance lacks (not_found: torrplayer without the schedule endpoint), a ref the server forgot
// (unknown_ref, after its restart). a failure the viewer can act on keeps the inline error row: a timeout, the
// upstream's 5xx, auth, the network
namespace cadence::screen::routes::home_answer {

enum class Outcome {
  // its rows
  Rows,
  // no row, asked again only on the slow re-check (a feed its instance may gain, a ref the next answer renews)
  Hide,
  // no row, never asked again while home lives (the provider cannot browse, or the call itself is wrong)
  HideForGood,
  // the inline error row with its retry
  Error,
};

// status is the rpc status, code the server's refusal code (catalog.RefusalOf: unsupported, not_found, unknown_ref,
// sign_in, unavailable, ...)
inline Outcome outcomeOf(std::string_view status, std::string_view code) {
  if (status == "ok") return Outcome::Rows;
  if (code == "unsupported" || status == "unknown_method") return Outcome::HideForGood;
  // invalid arguments: asking again would only be refused again
  if (status == "invalid") return Outcome::HideForGood;
  if (status == "not_found" || code == "not_found" || status == "expired" || code == "unknown_ref") return Outcome::Hide;
  return Outcome::Error;
}

}  // namespace cadence::screen::routes::home_answer

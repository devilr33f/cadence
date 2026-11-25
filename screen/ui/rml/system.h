#pragma once

#include "i18n/translator.h"

#include <RmlUi/Core/SystemInterface.h>

#include <array>
#include <cstdint>
#include <string>

namespace cadence::screen::ui {

// rmlui's system interface: its clock is the ui's (set by update, so a test or the gallery steps time exactly),
// TranslateString is the i18n translator's [[key]] rewrite, and its log is counted, never written out (rule 8:
// rmlui's messages can quote document text). "gen:" sources (sprites, qr codes) are never joined to a document path
class RmlSystem final : public Rml::SystemInterface {
 public:
  explicit RmlSystem(i18n::Translator& translator) : translator_(translator) {}

  void setNowMs(std::int64_t nowMs) { nowMs_ = nowMs; }
  [[nodiscard]] std::int64_t nowMs() const { return nowMs_; }

  double GetElapsedTime() override { return static_cast<double>(nowMs_) / 1000.0; }
  int TranslateString(Rml::String& translated, const Rml::String& input) override;
  void JoinPath(Rml::String& translated, const Rml::String& documentPath, const Rml::String& path) override;
  bool LogMessage(Rml::Log::Type type, const Rml::String& message) override;

  struct LogCounts {
    std::uint64_t errors = 0;
    std::uint64_t warnings = 0;
  };
  [[nodiscard]] LogCounts counts() const { return counts_; }
  // the last error's text, for tests and the gallery's own output; never logged
  [[nodiscard]] const std::string& lastError() const { return lastError_; }

 private:
  i18n::Translator& translator_;
  std::int64_t nowMs_ = 0;
  LogCounts counts_;
  std::string lastError_;
};

}  // namespace cadence::screen::ui

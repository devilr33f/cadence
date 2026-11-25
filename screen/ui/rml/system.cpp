#include "ui/rml/system.h"

namespace cadence::screen::ui {

int RmlSystem::TranslateString(Rml::String& translated, const Rml::String& input) {
  if (input.find("[[") == Rml::String::npos) {
    translated = input;
    return 0;
  }
  return translator_.translate(translated, input);
}

void RmlSystem::JoinPath(Rml::String& translated, const Rml::String& documentPath, const Rml::String& path) {
  if (path.starts_with("gen:")) {
    translated = path;
    return;
  }
  Rml::SystemInterface::JoinPath(translated, documentPath, path);
}

bool RmlSystem::LogMessage(Rml::Log::Type type, const Rml::String& message) {
  switch (type) {
    case Rml::Log::LT_ERROR:
    case Rml::Log::LT_ASSERT:
      counts_.errors++;
      // the errors since start, capped: tests and the gallery print them, nothing logs them
      if (lastError_.size() < 4096) lastError_ += (lastError_.empty() ? "" : " | ") + message;
      break;
    case Rml::Log::LT_WARNING:
      counts_.warnings++;
      break;
    default:
      break;
  }
  return true;
}

}  // namespace cadence::screen::ui

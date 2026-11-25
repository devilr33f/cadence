// built only by the tests that expect its build to fail: each case names what keys.h refuses
#include "i18n/gen/keys.h"
#include "i18n/translator.h"

int main() {
  cadence::i18n::Translator t;
#if defined(CADENCE_I18N_MISSING_KEY)
  return static_cast<int>(t.tr(cadence::i18n::keys::PlayerOsdNoSuchKey{}).size());
#elif defined(CADENCE_I18N_WRONG_ARG)
  return static_cast<int>(t.tr(cadence::i18n::keys::TasksCount{"three"}).size());
#elif defined(CADENCE_I18N_MISSING_ARG)
  return static_cast<int>(t.tr(cadence::i18n::keys::AudioUpNext{}).size());
#else
  return static_cast<int>(t.tr(cadence::i18n::keys::TasksCount{3}).size());
#endif
}

#include "text.h"

#include <catch2/catch_test_macros.hpp>

#include <string>

using namespace cadence::text;

TEST_CASE("bounds foreign messages to printable ascii") {
  CHECK(boundedMessage(nullptr).empty());
  CHECK(boundedMessage("open: no such file") == "open: no such file");
  CHECK(boundedMessage("tab\there\nbyte\x7f\xd1\x8f") == "tab?here?byte???");

  const std::string longer(kMaxBoundedLength + 10, 'x');
  CHECK(boundedMessage(longer.c_str()) == std::string(kMaxBoundedLength, 'x'));

  // a buffer with no terminator inside the limit is never read past it
  std::string unterminated(kMaxBoundedLength, 'y');
  CHECK(boundedMessage(unterminated.data()).size() == kMaxBoundedLength);
}

TEST_CASE("bounds text to whole utf-8 characters, mending broken ones") {
  CHECK(boundedUtf8("", 4).empty());
  CHECK(boundedUtf8("caf\xc3\xa9 \xe2\x82\xac \xf0\x9f\x8e\xb5", 64) == "caf\xc3\xa9 \xe2\x82\xac \xf0\x9f\x8e\xb5");
  // a character that would end past the bound is left out whole
  CHECK(boundedUtf8("ab\xc3\xa9", 3) == "ab");
  CHECK(boundedUtf8("ab\xc3\xa9", 4) == "ab\xc3\xa9");
  CHECK(boundedUtf8("a\xf0\x9f\x8e\xb5", 4) == "a");
  CHECK(boundedUtf8("abcdef", 4) == "abcd");
  // a truncated sequence, a stray continuation, an overlong form, a surrogate and a code point past U+10FFFF
  CHECK(boundedUtf8("x\xc3", 8) == "x\xef\xbf\xbd");
  CHECK(boundedUtf8("\x80y", 8) == "\xef\xbf\xbdy");
  CHECK(boundedUtf8("\xc0\xaf", 8) == "\xef\xbf\xbd\xef\xbf\xbd");
  CHECK(boundedUtf8("\xed\xa0\x80", 9) == "\xef\xbf\xbd\xef\xbf\xbd\xef\xbf\xbd");
  CHECK(boundedUtf8("\xf4\x90\x80\x80", 12) == "\xef\xbf\xbd\xef\xbf\xbd\xef\xbf\xbd\xef\xbf\xbd");
  // a replacement that does not fit is left out too
  CHECK(boundedUtf8("ab\xff", 4) == "ab");
}

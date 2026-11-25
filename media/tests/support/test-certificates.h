#pragma once

#include "support/http-server.h"

#include <string>

namespace cadence::test_support {

// a certificate authority made for one test run, and leaves it signed: nothing a real client would trust
struct TestCertificates {
  // the authority, as a ca bundle holds it
  std::string authorityPem;
  // for 127.0.0.1
  TlsIdentity server;
  // for other.example only, so a client that dialled 127.0.0.1 must refuse it
  TlsIdentity otherHost;
};

TestCertificates makeTestCertificates();

}  // namespace cadence::test_support

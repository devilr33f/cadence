#include "support/test-certificates.h"

#include <openssl/crypto.h>
#include <openssl/evp.h>
#include <openssl/pem.h>
#include <openssl/x509.h>
#include <openssl/x509v3.h>

#include <memory>
#include <stdexcept>

namespace cadence::test_support {

namespace {

using Key = std::unique_ptr<EVP_PKEY, decltype(&EVP_PKEY_free)>;
using Certificate = std::unique_ptr<X509, decltype(&X509_free)>;
using Bio = std::unique_ptr<BIO, decltype(&BIO_free)>;

void require(bool ok, const char* what) {
  if (!ok)
    throw std::runtime_error(what);
}

Key newKey() {
  Key key(EVP_PKEY_Q_keygen(nullptr, nullptr, "EC", "P-256"), EVP_PKEY_free);
  require(key != nullptr, "key");
  return key;
}

void addExtension(X509* certificate, X509* issuer, int nid, const char* value) {
  X509V3_CTX context;
  X509V3_set_ctx_nodb(&context);
  X509V3_set_ctx(&context, issuer, certificate, nullptr, nullptr, 0);
  X509_EXTENSION* extension = X509V3_EXT_conf_nid(nullptr, &context, nid, value);
  require(extension != nullptr, "extension");
  X509_add_ext(certificate, extension, -1);
  X509_EXTENSION_free(extension);
}

// signed by issuer with issuerKey, or by itself when issuer is null
Certificate newCertificate(EVP_PKEY* key, const char* commonName, long serial, X509* issuer, EVP_PKEY* issuerKey,
                           const char* altName) {
  Certificate certificate(X509_new(), X509_free);
  require(certificate != nullptr, "certificate");
  auto* raw = certificate.get();
  X509_set_version(raw, X509_VERSION_3);
  ASN1_INTEGER_set(X509_get_serialNumber(raw), serial);
  X509_gmtime_adj(X509_getm_notBefore(raw), -3600);
  X509_gmtime_adj(X509_getm_notAfter(raw), 24 * 3600);
  X509_set_pubkey(raw, key);
  auto* name = X509_get_subject_name(raw);
  X509_NAME_add_entry_by_txt(name, "CN", MBSTRING_ASC, reinterpret_cast<const unsigned char*>(commonName), -1, -1, 0);
  X509_set_issuer_name(raw, issuer != nullptr ? X509_get_subject_name(issuer) : name);
  auto* signer = issuer != nullptr ? issuer : raw;
  addExtension(raw, signer, NID_subject_key_identifier, "hash");
  if (issuer == nullptr) {
    addExtension(raw, signer, NID_basic_constraints, "critical,CA:TRUE");
    addExtension(raw, signer, NID_key_usage, "critical,keyCertSign,cRLSign");
  } else {
    addExtension(raw, signer, NID_authority_key_identifier, "keyid:always");
    addExtension(raw, signer, NID_basic_constraints, "critical,CA:FALSE");
    addExtension(raw, signer, NID_key_usage, "critical,digitalSignature");
    addExtension(raw, signer, NID_ext_key_usage, "serverAuth");
    addExtension(raw, signer, NID_subject_alt_name, altName);
  }
  require(X509_sign(raw, issuerKey != nullptr ? issuerKey : key, EVP_sha256()) > 0, "sign");
  return certificate;
}

template <typename Write>
std::string pem(Write write) {
  Bio bio(BIO_new(BIO_s_mem()), BIO_free);
  require(bio != nullptr && write(bio.get()) == 1, "pem");
  char* data = nullptr;
  const auto size = BIO_get_mem_data(bio.get(), &data);
  return {data, static_cast<std::size_t>(size)};
}

std::string pem(X509* certificate) {
  return pem([certificate](BIO* bio) { return PEM_write_bio_X509(bio, certificate); });
}

std::string pem(EVP_PKEY* key) {
  return pem([key](BIO* bio) { return PEM_write_bio_PrivateKey(bio, key, nullptr, nullptr, 0, nullptr, nullptr); });
}

}  // namespace

TestCertificates makeTestCertificates() {
  // the first use of openssl decides: no cleanup at exit, as in audiod
  OPENSSL_init_crypto(OPENSSL_INIT_NO_ATEXIT, nullptr);
  const auto authorityKey = newKey();
  const auto authority = newCertificate(authorityKey.get(), "cadence test ca", 1, nullptr, nullptr, nullptr);
  const auto serverKey = newKey();
  const auto server =
      newCertificate(serverKey.get(), "127.0.0.1", 2, authority.get(), authorityKey.get(), "IP:127.0.0.1");
  const auto otherKey = newKey();
  const auto other =
      newCertificate(otherKey.get(), "other.example", 3, authority.get(), authorityKey.get(), "DNS:other.example");
  return {pem(authority.get()), {pem(server.get()), pem(serverKey.get())}, {pem(other.get()), pem(otherKey.get())}};
}

}  // namespace cadence::test_support

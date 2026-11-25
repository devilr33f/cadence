package yandex

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testCA struct {
	cert  *x509.Certificate
	key   *ecdsa.PrivateKey
	roots *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "cadence test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &testCA{cert: cert, key: key, roots: roots}
}

func (c *testCA) files(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "ca.pem")
	keyFile := filepath.Join(dir, "ca.key")
	keyDER, err := x509.MarshalECPrivateKey(c.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func (c *testCA) leaf(t *testing.T, hosts ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: hosts[0]}, DNSNames: hosts,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, c.cert, key.Public(), c.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}
}

func TestCertificateSendsSigningChain(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.files(t)
	signer, err := loadCA(certFile, keyFile, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := signer.certificate(&tls.ClientHelloInfo{ServerName: quasarNet})
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.Certificate) != 2 || !bytes.Equal(leaf.Certificate[1], ca.cert.Raw) {
		t.Fatalf("certificate chain has %d elements", len(leaf.Certificate))
	}
}

func TestLoadCARequiresPrivatePermissionsAndSigningCertificate(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.files(t)
	if _, err := loadCA(certFile, keyFile, time.Now); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCA(certFile, keyFile, time.Now); err == nil {
		t.Fatal("group-readable key accepted")
	}
}

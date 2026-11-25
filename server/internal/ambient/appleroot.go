package ambient

import (
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"errors"
)

// sylvan.apple.com chains to Apple Root CA, which the mozilla bundle in the server image lacks (macOS trusts it,
// so a fetch from a mac works). public certificate from https://www.apple.com/appleca/, sha-256 pinned in the test.
//
//go:embed apple_root_ca.pem
var appleRootPEM []byte

// appleRoot is the embedded Apple Root CA
func appleRoot() (*x509.Certificate, error) {
	b, _ := pem.Decode(appleRootPEM)
	if b == nil {
		return nil, errors.New("ambient: apple root: no pem")
	}
	return x509.ParseCertificate(b.Bytes)
}

// manifestRoots is the system's roots with Apple Root CA added
func manifestRoots() (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	c, err := appleRoot()
	if err != nil {
		return nil, err
	}
	pool.AddCert(c)
	return pool, nil
}

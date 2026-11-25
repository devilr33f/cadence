package link

import (
	"crypto/x509"
	"errors"
	"os"
)

// RootCAs is nil, the system pool, without a ca file, else the system pool
// plus the file's certificates. Its errors never quote the path.
func RootCAs(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // the path is the operator's own config
	if err != nil {
		return nil, errors.New("link: cannot read ca file")
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("link: ca file has no certificates")
	}
	return pool, nil
}

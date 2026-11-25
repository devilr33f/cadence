package yandex

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"
)

var errCA = errors.New("yandex proxy: unusable ca")

type signingCA struct {
	cert   *x509.Certificate
	chain  [][]byte
	key    crypto.Signer
	now    func() time.Time
	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

func loadCA(certFile, keyFile string, now func() time.Time) (*signingCA, error) {
	if st, err := os.Stat(keyFile); err != nil || st.Mode().Perm()&0o077 != 0 {
		return nil, errCA
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errCA
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !cert.IsCA || !cert.BasicConstraintsValid {
		return nil, errCA
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errCA
	}
	key, ok := pair.PrivateKey.(crypto.Signer)
	if !ok || now().Before(cert.NotBefore) || !now().Before(cert.NotAfter) {
		return nil, errCA
	}
	return &signingCA{cert: cert, chain: pair.Certificate, key: key, now: now, leaves: map[string]*tls.Certificate{}}, nil
}

func (c *signingCA) tlsConfig() *tls.Config {
	return &tls.Config{ //nolint:gosec // tls 1.2 at most: quasar fails go's tls 1.3 (below)
		GetCertificate: c.certificate,
		NextProtos:     []string{"http/1.1"},
		MinVersion:     tls.VersionTLS12,
		// quasar closes during go's tls 1.3 certificate flight on the tested firmware
		MaxVersion: tls.VersionTLS12,
	}
}

func (c *signingCA) certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := canonicalHost(hello.ServerName)
	if !allowedHost(host) {
		return nil, fmt.Errorf("host not provisioned")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cert := c.leaves[host]; cert != nil && cert.Leaf != nil && c.now().Before(cert.Leaf.NotAfter.Add(-time.Hour)) {
		return cert, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, errCA
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return nil, errCA
	}
	now := c.now()
	notAfter := now.Add(30 * 24 * time.Hour)
	if c.cert.NotAfter.Before(notAfter) {
		notAfter = c.cert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, c.cert, key.Public(), c.key)
	if err != nil {
		return nil, errCA
	}
	leaf, _ := x509.ParseCertificate(der)
	cert := &tls.Certificate{Certificate: append([][]byte{der}, c.chain...), PrivateKey: key, Leaf: leaf}
	c.leaves[host] = cert
	return cert, nil
}

func canonicalHost(authority string) string {
	authority = strings.TrimSpace(strings.ToLower(authority))
	authority = strings.TrimSuffix(authority, ".")
	if strings.HasPrefix(authority, "[") {
		if end := strings.Index(authority, "]"); end >= 0 {
			return authority[1:end]
		}
	}
	if colon := strings.LastIndexByte(authority, ':'); colon >= 0 && strings.Count(authority, ":") == 1 {
		authority = authority[:colon]
	}
	return strings.TrimSuffix(authority, ".")
}

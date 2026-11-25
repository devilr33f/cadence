package ambient

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"testing"
)

// the cluster's fetch failed tls (ambient_manifest_failed code=network): sylvan's chain ends at Apple Root CA, which
// the server image's bundle lacks
func TestDefaultFetcherTrustsAppleRootCA(t *testing.T) {
	root, err := appleRoot()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(root.Raw)
	if got := hex.EncodeToString(sum[:]); got != "b0b1730ecbc7ff4505142c49f1295e6eda6bcaed7e2c68c5be91b5a11001f024" {
		t.Fatalf("embedded root sha-256 %s", got)
	}
	f, err := NewFetcher(FetcherConfig{})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := f.client.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("default fetcher has no root pool of its own")
	}
	if _, err := root.Verify(x509.VerifyOptions{Roots: tr.TLSClientConfig.RootCAs}); err != nil {
		t.Fatalf("apple root not trusted by the fetcher: %v", err)
	}
}

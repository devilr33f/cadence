package install

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"
)

// testCA is a self-signed CA certificate as PEM, as caddy's local authority writes its root
func testCA(t *testing.T, name string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestExtraCA(t *testing.T) {
	one, two := testCA(t, "one"), testCA(t, "two")
	for name, in := range map[string][]byte{"one": one, "commented": append([]byte("# my lan ca\n"), one...)} {
		got, err := ExtraCA(in)
		if err != nil || string(got) != string(one) {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	if got, err := ExtraCA(append(slices.Clone(one), two...)); err != nil || string(got) != string(one)+string(two) {
		t.Errorf("two: %q, %v", got, err)
	}
	der, _ := pem.Decode(one)
	for name, in := range map[string][]byte{
		"empty": nil, "junk": []byte("not a certificate"), "der": der.Bytes,
		"key":     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")}),
		"garbled": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbled")}),
		"mixed":   append(slices.Clone(one), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")})...),
	} {
		if _, err := ExtraCA(in); !errors.Is(err, ErrRefused) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// the server's own ca joins the tarball's bundle before enroll, which reads it
func TestInstallAddsTheExtraCA(t *testing.T) {
	f := v1Speaker(t)
	o := runOpts(t, f)
	o.ExtraCA = testCA(t, "lan")
	out, err := install(t, f, o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := string(f.get(f.l.ca)), "pem\n"+string(o.ExtraCA); got != want {
		t.Fatalf("bundle %q, want %q", got, want)
	}
	ops := f.changes()
	wrote := slices.Index(ops, "write "+f.l.ca+" 644")
	enrolled := slices.IndexFunc(ops, func(op string) bool { return strings.HasPrefix(op, "enroll ") })
	if wrote < 0 || enrolled < 0 || wrote > enrolled {
		t.Fatalf("the bundle was not written before enroll:\n%s", strings.Join(ops, "\n"))
	}
	if !strings.Contains(out, "extra CA added to "+f.l.ca) || strings.Contains(out, "reboot the speaker") {
		t.Fatalf("output:\n%s", out)
	}
}

// a ca first named on a later run, the speaker staged and enrolled already, is still added, and only once
func TestExtraCAOnALaterRun(t *testing.T) {
	f := v1Speaker(t)
	mustInstall(t, f)
	o := runOpts(t, f)
	o.ExtraCA = testCA(t, "lan")
	for run, writes := range []int{1, 0} {
		f.ops = nil
		out, err := install(t, f, o)
		if err != nil {
			t.Fatalf("run %d: %v\n%s", run, err, out)
		}
		// cadenced read its roots when it started: a running one keeps the old bundle until it restarts
		if strings.Contains(out, "reboot the speaker") != (writes == 1) {
			t.Fatalf("run %d: the restart advice is wrong:\n%s", run, out)
		}
		if got := slices.Index(f.changes(), "write "+f.l.ca+" 644"); (got >= 0) != (writes == 1) {
			t.Fatalf("run %d wrote the bundle %v:\n%s", run, got >= 0, strings.Join(f.changes(), "\n"))
		}
		if got, want := string(f.get(f.l.ca)), "pem\n"+string(o.ExtraCA); got != want {
			t.Fatalf("run %d: bundle %q", run, got)
		}
	}
}

func TestDryRunNamesTheExtraCA(t *testing.T) {
	f := v1Speaker(t)
	o := runOpts(t, f)
	o.ExtraCA, o.DryRun = testCA(t, "lan"), true
	out, err := install(t, f, o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "would add the extra CA to "+f.l.ca) || strings.Contains(string(f.get(f.l.ca)), "CERTIFICATE") {
		t.Fatalf("bundle %q\n%s", f.get(f.l.ca), out)
	}
}

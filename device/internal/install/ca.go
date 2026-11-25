package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
)

// ExtraCA checks an owner's CA file (a quickstart server's own authority) and returns its certificates as PEM, the
// text around them dropped. A block that is not a certificate refuses the whole file: a key never reaches a device.
func ExtraCA(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	for rest := raw; ; {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("%w: the CA file holds a %s, not only certificates", ErrRefused, b.Type)
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			return nil, fmt.Errorf("%w: the CA file holds a certificate that does not parse", ErrRefused)
		}
		if err := pem.Encode(&out, &pem.Block{Type: b.Type, Bytes: b.Bytes}); err != nil {
			return nil, err
		}
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("%w: the CA file holds no PEM certificate", ErrRefused)
	}
	return out.Bytes(), nil
}

// trustCA puts the tarball's bundle with the extra CA after it at the bundle cadenced, audiod and soloist read. it runs
// before every enroll phase, done or not, so a CA first named on a later run still lands; hot swaps replace only bin/
func (in *Installer) trustCA(ctx context.Context) error {
	if len(in.o.ExtraCA) == 0 {
		return nil
	}
	if in.o.DryRun {
		in.say("would add the extra CA to %s", in.l.ca)
		return nil
	}
	want := bytes.Clone(in.o.Bundle.CA)
	if len(want) > 0 && want[len(want)-1] != '\n' {
		want = append(want, '\n')
	}
	want = append(want, in.o.ExtraCA...)
	sum := sha256.Sum256(want)
	if s, ok, err := in.dev.Sum(ctx, in.l.ca); err != nil || (ok && s == hex.EncodeToString(sum[:])) {
		return err
	}
	if err := in.dev.Write(ctx, in.l.ca, want, 0o644); err != nil {
		return err
	}
	in.say("extra CA added to %s", in.l.ca)
	// cadenced reads its roots once at start, and verify counts the link_up it logged before
	if in.done("start-v2") {
		in.say("the running cadenced still trusts the old bundle: reboot the speaker for it to take the new CA")
	}
	return nil
}

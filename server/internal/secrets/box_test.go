package secrets_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
)

func newBox(t *testing.T) *secrets.Box {
	t.Helper()
	b, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := newBox(t)
	sealed, err := b.Seal([]byte("plain"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	if sealed[0] != 1 || int(sealed[1]) != 8 {
		t.Fatalf("layout %v", sealed[:2])
	}
	got, err := b.Open(sealed, []byte("aad"))
	if err != nil || string(got) != "plain" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestOpenWrongAAD(t *testing.T) {
	b := newBox(t)
	sealed, _ := b.Seal([]byte("plain"), []byte("aad"))
	if _, err := b.Open(sealed, []byte("other")); err == nil {
		t.Fatal("want error")
	}
}

func TestOpenTampered(t *testing.T) {
	b := newBox(t)
	sealed, _ := b.Seal([]byte("plain"), nil)
	sealed[len(sealed)-1] ^= 1
	if _, err := b.Open(sealed, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestOpenRejectsGarbage(t *testing.T) {
	b := newBox(t)
	for _, in := range [][]byte{nil, {1}, {2, 8}, {1, 200, 1, 2}} {
		if _, err := b.Open(in, nil); err == nil {
			t.Fatalf("want error for %v", in)
		}
	}
}

func TestNewRejectsShortKey(t *testing.T) {
	if _, err := secrets.New(make([]byte, 16)); err == nil {
		t.Fatal("want error")
	}
}

func writeKey(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func reason(t *testing.T, err error) string {
	t.Helper()
	var ce config.Error
	if !errors.As(err, &ce) || ce.Variable != "CADENCE_MASTER_KEY_FILE" {
		t.Fatalf("err = %v", err)
	}
	return ce.Reason
}

func TestLoadOK(t *testing.T) {
	key := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=\n"
	if _, err := secrets.Load(writeKey(t, key, 0o600)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUnreadable(t *testing.T) {
	_, err := secrets.Load(filepath.Join(t.TempDir(), "missing"))
	if r := reason(t, err); r != "unreadable" {
		t.Fatal(r)
	}
}

func TestLoadBadContent(t *testing.T) {
	for _, c := range []string{"not base64 !!", "AAAA"} {
		_, err := secrets.Load(writeKey(t, c, 0o600))
		if r := reason(t, err); r != "must hold base64 of 32 bytes" {
			t.Fatal(r)
		}
		if strings.Contains(err.Error(), c) {
			t.Fatal("error leaks content")
		}
	}
}

func TestLoadRejectsOpenMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix modes")
	}
	p := writeKey(t, "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=\n", 0o644)
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := secrets.Load(p)
	if r := reason(t, err); r != "must not be readable by group or others" {
		t.Fatal(r)
	}
	if strings.Contains(err.Error(), p) {
		t.Fatal("error leaks path")
	}
}

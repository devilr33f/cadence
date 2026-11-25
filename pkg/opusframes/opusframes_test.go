package opusframes_test

import (
	"bytes"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/opusframes"
)

func TestRoundTrip(t *testing.T) {
	in := [][]byte{{1}, {2, 3}, {4, 5, 6}}
	var buf bytes.Buffer
	for _, p := range in {
		if err := opusframes.Write(&buf, p); err != nil {
			t.Fatal(err)
		}
	}
	out, err := opusframes.Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || !bytes.Equal(out[2], in[2]) || !bytes.Equal(out[0], in[0]) {
		t.Fatalf("got %v", out)
	}
}

func TestTruncated(t *testing.T) {
	b := append([]byte{10, 0}, 1, 2, 3, 4)
	if _, err := opusframes.Read(bytes.NewReader(b)); err == nil {
		t.Fatal("want error")
	}
}

func TestZeroLength(t *testing.T) {
	if _, err := opusframes.Read(bytes.NewReader([]byte{0, 0})); err == nil {
		t.Fatal("want error")
	}
	if err := opusframes.Write(&bytes.Buffer{}, nil); err == nil {
		t.Fatal("want error")
	}
}

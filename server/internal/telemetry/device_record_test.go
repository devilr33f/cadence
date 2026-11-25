package telemetry_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// framed wraps an encoded LogRecord in a request as the device frames its ring's bytes
func framed(record []byte) []byte {
	field := func(num protowire.Number, b []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), b)
	}
	return field(1, field(2, field(2, record)))
}

// a record as the device's encode writes it (pinned in proto/fixtures by the
// device's own test) is stored with its flags
func TestDeviceEncodedRecordKeepsItsFlags(t *testing.T) {
	r := newRig(t)
	rec, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "fixtures", "otlp", "v1", "log_record_cut.binpb"))
	if err != nil {
		t.Fatal(err)
	}
	if a := r.post(logsPath, gz(framed(rec)), gzipped(tokenA)); a.status != 200 || len(a.body) != 0 {
		t.Fatalf("%d %q", a.status, a.body)
	}
	got := r.storedLogs()
	if len(got) != 1 {
		t.Fatalf("stored %d", len(got))
	}
	g := got[0]
	if !g.ts.Equal(start.Add(-time.Second)) || g.source != "soloist" || g.severity != 13 || g.body != "…mid li…" ||
		!g.truncated || !g.continued || g.device == nil || *g.device != r.devA {
		t.Fatalf("stored %+v", g)
	}
}

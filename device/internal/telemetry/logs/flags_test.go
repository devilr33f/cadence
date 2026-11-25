package logs_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
)

var update = flag.Bool("update", false, "rewrite the golden device-encoded record")

// the record the server's ingest test stores, as this encode writes it
var golden = filepath.Join("..", "..", "..", "..", "proto", "fixtures", "otlp", "v1", "log_record_cut.binpb")

// flags are the bool attributes of r other than source, by key
func flags(r *logsv1.LogRecord) map[string]bool {
	out := map[string]bool{}
	for _, kv := range r.GetAttributes() {
		if kv.GetKey() != "source" {
			out[kv.GetKey()] = kv.GetValue().GetBoolValue()
		}
	}
	return out
}

// a cut line says so, so the server keeps the flags and redacts its ends again
func TestFlagsShipAsAttributes(t *testing.T) {
	p, _ := offline(t)
	cases := []struct {
		name string
		r    logs.Record
		want map[string]bool
	}{
		{"whole", logs.Record{Severity: logs.Info, Source: "s", Body: "whole"}, map[string]bool{}},
		{"truncated", logs.Record{Severity: logs.Info, Source: "s", Body: "half a li…", Truncated: true}, map[string]bool{"truncated": true}},
		{"continued", logs.Record{Severity: logs.Info, Source: "s", Body: "…ne", Continued: true}, map[string]bool{"continued": true}},
		{"both", logs.Record{Severity: logs.Info, Source: "s", Body: "…mid…", Truncated: true, Continued: true},
			map[string]bool{"truncated": true, "continued": true}},
		// the record's own cut at 4096 bytes is a cut too
		{"cut here", logs.Record{Severity: logs.Info, Source: "s", Body: strings.Repeat("a", 5000)}, map[string]bool{"truncated": true}},
	}
	for _, c := range cases {
		got := flags(one(t, p, c.r))
		if len(got) != len(c.want) {
			t.Fatalf("%s: attributes %v", c.name, got)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Fatalf("%s: attributes %v", c.name, got)
			}
		}
	}
}

// the encoded bytes are pinned in proto/fixtures, where the server's ingest test reads them
func TestCutRecordGolden(t *testing.T) {
	p, _ := offline(t)
	p.Emit(logs.Record{Time: t0.Add(-time.Second), Severity: logs.Warn, Source: "soloist", Body: "…mid li…", Truncated: true, Continued: true})
	raw := logs.HeldRaw(p)
	if len(raw) != 1 {
		t.Fatalf("%d records", len(raw))
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, raw[0], 0o644); err != nil { //nolint:gosec // a test fixture
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw[0], want) {
		t.Fatalf("encoded %x, golden %x (rewrite with -update)", raw[0], want)
	}
}

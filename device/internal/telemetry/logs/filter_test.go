package logs_test

import (
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

func kept(t *testing.T, p *logs.Pipeline, r logs.Record) bool {
	t.Helper()
	before := len(held(t, p))
	p.Emit(r)
	return len(held(t, p)) > before
}

func TestFilterDefaultInfo(t *testing.T) {
	p, _ := offline(t)
	if kept(t, p, logs.Record{Severity: logs.Debug, Source: "cadenced", Body: "d"}) {
		t.Fatal("debug kept at the default level")
	}
	if !kept(t, p, logs.Record{Severity: logs.Info, Source: "cadenced", Body: "i"}) {
		t.Fatal("info dropped at the default level")
	}
}

func TestFilterPerSourceLiveChange(t *testing.T) {
	p, _ := offline(t)
	levels := map[string]logs.Severity{"kmsg": logs.Warn, "soloist": logs.Debug}
	p.SetLevels(levels, logs.Info)
	// the pipeline keeps its own copy
	levels["kmsg"] = logs.Debug
	for _, c := range []struct {
		r    logs.Record
		want bool
	}{
		{logs.Record{Severity: logs.Info, Source: "kmsg"}, false},
		{logs.Record{Severity: logs.Warn, Source: "kmsg"}, true},
		{logs.Record{Severity: logs.Debug, Source: "soloist"}, true},
		{logs.Record{Severity: logs.Debug, Source: "cadenced"}, false},
		{logs.Record{Severity: logs.Info, Source: "cadenced"}, true},
	} {
		if got := kept(t, p, c.r); got != c.want {
			t.Fatalf("%s at %d: kept = %v", c.r.Source, c.r.Severity, got)
		}
	}
	p.SetLevels(nil, logs.Error)
	if kept(t, p, logs.Record{Severity: logs.Warn, Source: "kmsg"}) || kept(t, p, logs.Record{Severity: logs.Warn, Source: "cadenced"}) {
		t.Fatal("warn kept at error")
	}
	if !kept(t, p, logs.Record{Severity: logs.Error, Source: "cadenced"}) {
		t.Fatal("error dropped at error")
	}
}

// a source shows in the stats once it emitted, filtered or not, so the level of a quiet one can be raised
func TestStatsSources(t *testing.T) {
	p, _ := offline(t)
	p.Emit(logs.Record{Severity: logs.Info, Source: "kmsg"})
	p.Emit(logs.Record{Severity: logs.Debug, Source: "audiod"})
	p.Emit(logs.Record{Severity: logs.Info, Source: "kmsg"})
	st := p.Stats()
	if !slices.Equal(st.Sources, []string{"audiod", "kmsg"}) {
		t.Fatalf("sources = %v", st.Sources)
	}
	if st.RingBytes == 0 || st.Dropped != 0 || st.Rejected != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

package settings

import (
	"encoding/json"
	"testing"
)

// music held one name before it took several: a stored or sent name alone
// reads as a list of it
func TestMusicReadsOneNameAsAList(t *testing.T) {
	for in, want := range map[string]string{`"ytm"`: `["ytm"]`, `["ytm","sp"]`: `["ytm","sp"]`, `[]`: `[]`, `""`: `""`, `null`: `null`} {
		o := Overrides{"music": json.RawMessage(in)}
		normalizeNames(o)
		if string(o["music"]) != want {
			t.Fatalf("%s: %s, want %s", in, o["music"], want)
		}
	}
	v, err := Merge(Overrides{"music": json.RawMessage(`"ytm"`)}, nil)
	if err != nil || len(v.Music) != 1 || v.Music[0] != "ytm" {
		t.Fatalf("%v %v", v.Music, err)
	}
	if _, err := parse([]byte(`{"music":"bad name!"}`), false); err == nil {
		t.Fatal("a bad name passed")
	}
	o, err := parse([]byte(`{"music":"ytm"}`), true)
	if err != nil || string(o["music"]) != `["ytm"]` {
		t.Fatalf("%s %v", o["music"], err)
	}
}

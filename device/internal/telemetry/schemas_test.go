package telemetry_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
)

func compile(t *testing.T, src string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("s.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("s.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func check(t *testing.T, s *jsonschema.Schema, doc string, valid bool) {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Validate(v) == nil; got != valid {
		t.Errorf("%s: valid = %v, want %v", doc, got, valid)
	}
}

func proc(extra string) string {
	return `{"pid":1,"comm":"cadenced","cpu":0.5,"rss":1024` + extra + `}`
}

func TestLogsConfig(t *testing.T) {
	s := compile(t, telemetry.LogsConfigSchema)
	long := strings.Repeat("a", 32)
	for doc, valid := range map[string]bool{
		`{}`: true,
		`{"sources":{"cadenced":"debug","shairport-sync":"error","a":"warn","a1-b":"info"},"kmsg":false,"thirdParty":true}`: true,
		`{"traces":false}`:                     true,
		`{"sources":{"` + long + `":"info"}}`:  true,
		`{"sources":{"` + long + `b":"info"}}`: false,
		`{"sources":{"Cadenced":"info"}}`:      false,
		`{"sources":{"1a":"info"}}`:            false,
		`{"sources":{"a_b":"info"}}`:           false,
		`{"sources":{"a":"trace"}}`:            false,
		`{"kmsg":"yes"}`:                       false,
		`{"thirdParty":1}`:                     false,
		`{"other":true}`:                       false,
		manySources(64):                        true,
		manySources(65):                        false,
	} {
		check(t, s, doc, valid)
	}
}

func TestLogsState(t *testing.T) {
	s := compile(t, telemetry.LogsStateSchema)
	for doc, valid := range map[string]bool{
		`{"dropped":0,"rejected":0,"ringBytes":0,"sources":[]}`:           true,
		`{"dropped":3,"rejected":1,"ringBytes":65536,"sources":["kmsg"]}`: true,
		`{"dropped":-1,"rejected":0,"ringBytes":0,"sources":[]}`:          false,
		`{"dropped":0,"rejected":0,"ringBytes":0}`:                        false,
		`{"dropped":0,"rejected":0,"ringBytes":0,"sources":[1]}`:          false,
		`{"dropped":0,"rejected":0,"ringBytes":0,"sources":[],"x":1}`:     false,
	} {
		check(t, s, doc, valid)
	}
}

func TestSystemMetricsConfig(t *testing.T) {
	s := compile(t, telemetry.SystemMetricsConfigSchema)
	for doc, valid := range map[string]bool{
		`{}`: true, `{"intervalS":10}`: true, `{"intervalS":300}`: true,
		`{"intervalS":9}`: false, `{"intervalS":301}`: false, `{"intervalS":30.5}`: false,
		`{"intervalS":"30"}`: false, `{"interval":30}`: false,
	} {
		check(t, s, doc, valid)
	}
}

func TestSystemMetricsState(t *testing.T) {
	s := compile(t, telemetry.SystemMetricsStateSchema)
	ten := strings.TrimSuffix(strings.Repeat(proc("")+",", 10), ",")
	eleven := ten + "," + proc("")
	for doc, valid := range map[string]bool{
		`{"topCpu":[],"topRss":[]}`:                                                                   true,
		`{"topCpu":[` + proc("") + `],"topRss":[` + proc("") + `]}`:                                   true,
		`{"topCpu":[` + ten + `],"topRss":[` + ten + `]}`:                                             true,
		`{"topCpu":[` + eleven + `],"topRss":[]}`:                                                     false,
		`{"topCpu":[],"topRss":[` + eleven + `]}`:                                                     false,
		`{"topCpu":[` + proc(`,"cmdline":"x"`) + `],"topRss":[]}`:                                     false,
		`{"topCpu":[{"pid":0,"comm":"a","cpu":0,"rss":0}],"topRss":[]}`:                               false,
		`{"topCpu":[{"pid":1,"comm":"a","cpu":-0.1,"rss":0}],"topRss":[]}`:                            false,
		`{"topCpu":[{"pid":1,"comm":"a","cpu":0,"rss":-1}],"topRss":[]}`:                              false,
		`{"topCpu":[{"pid":1,"comm":"` + strings.Repeat("a", 32) + `","cpu":0,"rss":0}],"topRss":[]}`: true,
		`{"topCpu":[{"pid":1,"comm":"` + strings.Repeat("a", 33) + `","cpu":0,"rss":0}],"topRss":[]}`: false,
		`{"topCpu":[{"pid":1,"comm":"a","cpu":0}],"topRss":[]}`:                                       false,
		`{"topCpu":[]}`: false,
	} {
		check(t, s, doc, valid)
	}
}

// a config naming n sources, as many as the pipeline keeps apart at most
func manySources(n int) string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf(`"s%d":"info"`, i)
	}
	return `{"sources":{` + strings.Join(names, ",") + `}}`
}

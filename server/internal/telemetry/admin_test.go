package telemetry_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

type logsAnswer struct {
	Logs []map[string]any `json:"logs"`
}

func (r *adminRig) logsQuery(query string) []map[string]any {
	r.t.Helper()
	st, _, body := r.get("/api/v1/logs"+query, adminToken)
	if st != 200 {
		r.t.Fatalf("%s: %d %s", query, st, body)
	}
	var a logsAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil || a.Logs == nil {
		r.t.Fatalf("%s: %v %s", query, err, body)
	}
	return a.Logs
}

func bodies(logs []map[string]any) []string {
	out := make([]string, len(logs))
	for i, l := range logs {
		out[i], _ = l["body"].(string)
	}
	return out
}

func TestLogsQuery(t *testing.T) {
	r := newAdminRig(t)
	server := pgtype.UUID{}
	r.insertLogs(
		line(r.devA, start.Add(-5*time.Minute), "cadenced", 9, "a info"),
		line(r.devA, start.Add(-4*time.Minute), "kmsg", 13, "a warn DISK full"),
		line(r.devB, start.Add(-3*time.Minute), "cadenced", 17, "b error 50% done"),
		line(server, start.Add(-2*time.Minute), "server", 9, `{"msg":"server_line"}`),
		line(r.devB, start.Add(-time.Minute), "soloist", 5, "b debug a_b 500"),
	)

	all := r.logsQuery("")
	if got := bodies(all); !slices.Equal(got, []string{"b debug a_b 500", `{"msg":"server_line"}`, "b error 50% done", "a warn DISK full", "a info"}) {
		t.Fatalf("newest first: %q", got)
	}
	a := all[3]
	if a["deviceId"] != r.devA.String() || a["deviceName"] != "kitchen" || a["board"] != "yandex-pickle" || a["source"] != "kmsg" ||
		a["severity"] != 13.0 || a["truncated"] != false || a["continued"] != false ||
		a["ts"] != start.Add(-4*time.Minute).Format(time.RFC3339) || a["observedTs"] != start.Add(-4*time.Minute+time.Second).Format(time.RFC3339) {
		t.Fatalf("device line %v", a)
	}
	for _, k := range []string{"deviceId", "deviceName", "board"} {
		if _, ok := all[1][k]; ok {
			t.Fatalf("server line has %s: %v", k, all[1])
		}
	}

	for query, want := range map[string][]string{
		"?device=" + r.devA.String():   {"a warn DISK full", "a info"},
		"?source=server":               {`{"msg":"server_line"}`},
		"?source=cadenced&level=error": {"b error 50% done"},
		"?level=warn":                  {"b error 50% done", "a warn DISK full"},
		"?level=trace":                 {"b debug a_b 500", `{"msg":"server_line"}`, "b error 50% done", "a warn DISK full", "a info"},
		"?q=disk+FULL":                 {"a warn DISK full"},
		"?q=" + url.QueryEscape("50%"): {"b error 50% done"},
		"?q=a_b":                       {"b debug a_b 500"},
		"?q=a%5Cb":                     {},
		"?limit=2":                     {"b debug a_b 500", `{"msg":"server_line"}`},
		// since inclusive, until exclusive
		"?since=" + url.QueryEscape(start.Add(-4*time.Minute).Format(time.RFC3339)) +
			"&until=" + url.QueryEscape(start.Add(-2*time.Minute).Format(time.RFC3339)): {"b error 50% done", "a warn DISK full"},
	} {
		if got := bodies(r.logsQuery(query)); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", query, got, want)
		}
	}

	// refusals are the api's error model, naming the parameter (M7a-D1)
	for query, field := range map[string]string{
		"?limit=0": "limit", "?limit=1001": "limit", "?limit=x": "limit", "?device=not-a-uuid": "device",
		"?level=loud": "level", "?since=yesterday": "since",
		"?q=" + strings.Repeat("a", 257): "q", "?q=%00": "q", "?source=" + strings.Repeat("s", 65): "source",
		"?since=" + url.QueryEscape(start.Format(time.RFC3339)) + "&until=" + url.QueryEscape(start.Add(-time.Hour).Format(time.RFC3339)): "until",
	} {
		want := `{"error":"invalid","path":"/query/` + field + `"}`
		if st, _, body := r.get("/api/v1/logs"+query, adminToken); st != 422 || strings.TrimSpace(body) != want {
			t.Errorf("%.60s: %d %s, want %s", query, st, body, want)
		}
	}
	if st, _, _ := r.get("/api/v1/logs", ""); st != 401 {
		t.Fatalf("no token: %d", st)
	}
	if st, _, _ := r.get("/api/v1/logs", "cdn_wrong"); st != 401 {
		t.Fatalf("wrong token: %d", st)
	}
}

func TestLogsQueryDefaultLimit(t *testing.T) {
	r := newAdminRig(t)
	rows := make([]telemetry.LogRow, 250)
	for i := range rows {
		rows[i] = line(r.devA, start.Add(time.Duration(i)*time.Millisecond), "cadenced", 9, fmt.Sprintf("line %03d", i))
	}
	r.insertLogs(rows...)
	got := bodies(r.logsQuery(""))
	if len(got) != 200 || got[0] != "line 249" || got[199] != "line 050" {
		t.Fatalf("%d lines, %q … %q", len(got), got[0], got[len(got)-1])
	}
	if got := r.logsQuery("?limit=1000"); len(got) != 250 {
		t.Fatalf("limit 1000: %d", len(got))
	}
}

type seriesAnswer struct {
	Series []struct {
		Name   string          `json:"name"`
		Kind   string          `json:"kind"`
		Attrs  json.RawMessage `json:"attrs"`
		Points []struct {
			TS    time.Time  `json:"ts"`
			Value float64    `json:"value"`
			Start *time.Time `json:"start"`
			Reset bool       `json:"reset"`
		} `json:"points"`
	} `json:"series"`
	Truncated bool `json:"truncated"`
}

func (r *adminRig) seriesOf(dev pgtype.UUID, query string) seriesAnswer {
	r.t.Helper()
	st, _, body := r.get("/api/v1/devices/"+dev.String()+"/metrics"+query, adminToken)
	if st != 200 {
		r.t.Fatalf("%s: %d %s", query, st, body)
	}
	var a seriesAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil || a.Series == nil {
		r.t.Fatalf("%v %s", err, body)
	}
	return a
}

func TestDeviceMetricSeries(t *testing.T) {
	r := newAdminRig(t)
	boot, reboot := start.Add(-2*time.Hour), start.Add(-20*time.Minute)
	sumAt := func(at, from time.Time, v float64) telemetry.MetricRow {
		p := point(r.devA, "cadence.sessions", telemetry.KindSum, `{"trigger":"wake"}`, at, v)
		p.Start = from
		return p
	}
	r.insertPoints(
		point(r.devA, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"1"}`, start.Add(-time.Minute), 0.5),
		point(r.devA, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"0"}`, start.Add(-2*time.Minute), 0.25),
		point(r.devA, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"0"}`, start.Add(-time.Minute), 0.75),
		sumAt(start.Add(-40*time.Minute), boot, 3),
		sumAt(start.Add(-30*time.Minute), boot, 5),
		// a decrease is a reset
		sumAt(start.Add(-25*time.Minute), boot, 1),
		// and so is a new start, though the value rose
		sumAt(start.Add(-10*time.Minute), reboot, 4),
		sumAt(start.Add(-5*time.Minute), reboot, 4),
		// past the 24 h a series reaches back
		point(r.devA, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"0"}`, start.Add(-25*time.Hour), 0.1),
		// another name and another device
		point(r.devA, "system.memory.used", telemetry.KindGauge, `{}`, start.Add(-time.Minute), 1024),
		point(r.devB, "system.cpu.utilization", telemetry.KindGauge, `{"cpu":"0"}`, start.Add(-time.Minute), 0.9),
	)

	a := r.seriesOf(r.devA, "?name=system.cpu.utilization&name=cadence.sessions&since="+
		url.QueryEscape(start.Add(-48*time.Hour).Format(time.RFC3339)))
	if a.Truncated || len(a.Series) != 3 {
		t.Fatalf("%+v", a)
	}
	sessions, cpu0, cpu1 := a.Series[0], a.Series[1], a.Series[2]
	if sessions.Name != "cadence.sessions" || sessions.Kind != "sum" || string(sessions.Attrs) != `{"trigger": "wake"}` && string(sessions.Attrs) != `{"trigger":"wake"}` {
		t.Fatalf("sessions %+v", sessions)
	}
	var resets []bool
	var values []float64
	for _, p := range sessions.Points {
		resets, values = append(resets, p.Reset), append(values, p.Value)
		if p.Start == nil {
			t.Fatalf("sum point without its start %+v", p)
		}
	}
	if !slices.Equal(values, []float64{3, 5, 1, 4, 4}) || !slices.Equal(resets, []bool{false, false, true, true, false}) {
		t.Fatalf("sum values %v resets %v", values, resets)
	}
	if !sessions.Points[3].Start.Equal(reboot) {
		t.Fatalf("start %v", sessions.Points[3].Start)
	}
	if cpu0.Name != "system.cpu.utilization" || cpu0.Kind != "gauge" || len(cpu0.Points) != 2 || cpu0.Points[0].Value != 0.25 ||
		cpu0.Points[1].Value != 0.75 || cpu0.Points[0].Start != nil || cpu0.Points[1].Reset {
		t.Fatalf("cpu0 %+v", cpu0)
	}
	if len(cpu1.Points) != 1 || cpu1.Points[0].Value != 0.5 || !cpu1.Points[0].TS.Equal(start.Add(-time.Minute)) {
		t.Fatalf("cpu1 %+v", cpu1)
	}

	// since defaults to the 24 h kept, and narrows
	if a := r.seriesOf(r.devA, "?name=system.cpu.utilization"); len(a.Series) != 2 || len(a.Series[0].Points) != 2 {
		t.Fatalf("default since %+v", a)
	}
	if a := r.seriesOf(r.devA, "?name=system.cpu.utilization&since="+url.QueryEscape(start.Add(-90*time.Second).Format(time.RFC3339))); len(a.Series) != 2 ||
		len(a.Series[0].Points) != 1 {
		t.Fatalf("narrow since %+v", a)
	}
	if a := r.seriesOf(r.devA, "?name=nothing.here"); len(a.Series) != 0 {
		t.Fatalf("unknown name %+v", a)
	}

	names := make([]string, 21)
	for i := range names {
		names[i] = fmt.Sprintf("name=n%d", i)
	}
	for query, field := range map[string]string{
		"": "name", "?name=": "name", "?" + strings.Join(names, "&"): "name", "?name=" + strings.Repeat("n", 256): "name",
		"?name=a&since=never": "since", "?name=%00": "name",
	} {
		want := `{"error":"invalid","path":"/query/` + field + `"}`
		if st, _, body := r.get("/api/v1/devices/"+r.devA.String()+"/metrics"+query, adminToken); st != 422 || strings.TrimSpace(body) != want {
			t.Errorf("%.60s: %d %s, want %s", query, st, body, want)
		}
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000009", "not-a-uuid"} {
		if st, _, body := r.get("/api/v1/devices/"+id+"/metrics?name=a", adminToken); st != 404 || strings.TrimSpace(body) != `{"error":"not_found"}` {
			t.Errorf("%s: %d %s", id, st, body)
		}
	}
	if st, _, _ := r.get("/api/v1/devices/"+r.devA.String()+"/metrics?name=a", ""); st != 401 {
		t.Fatalf("no token: %d", st)
	}
}

func TestDeviceProcesses(t *testing.T) {
	r := newAdminRig(t)
	// a state carries more than the two lists only if something else wrote
	// it: never passed on
	r.setState(r.devA, "system.metrics", `{"topCpu":[{"pid":7,"comm":"cadenced","cpu":12.5,"rss":4096,"cmdline":"x"}],`+
		`"topRss":[{"pid":9,"comm":"soloist","cpu":1,"rss":81920}],"cmdline":"cadenced --token secret"}`)
	r.setState(r.devA, "logs", `{"dropped":0}`)
	st, _, body := r.get("/api/v1/devices/"+r.devA.String()+"/processes", adminToken)
	want := `{"topCpu":[{"pid":7,"comm":"cadenced","cpu":12.5,"rss":4096}],"topRss":[{"pid":9,"comm":"soloist","cpu":1,"rss":81920}]}`
	if st != 200 || strings.TrimSpace(body) != want {
		t.Fatalf("%d %s", st, body)
	}
	if st, _, body := r.get("/api/v1/devices/"+r.devB.String()+"/processes", adminToken); st != 200 || strings.TrimSpace(body) != `{"topCpu":[],"topRss":[]}` {
		t.Fatalf("none reported: %d %s", st, body)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000009", "not-a-uuid"} {
		if st, _, body := r.get("/api/v1/devices/"+id+"/processes", adminToken); st != 404 || strings.TrimSpace(body) != `{"error":"not_found"}` {
			t.Errorf("%s: %d %s", id, st, body)
		}
	}
	if st, _, _ := r.get("/api/v1/devices/"+r.devA.String()+"/processes", ""); st != 401 {
		t.Fatalf("no token: %d", st)
	}
	// the lists stay what the device's schema allows: 10 each, comm at 32 bytes on a rune
	var procs []string
	for i := range 12 {
		procs = append(procs, fmt.Sprintf(`{"pid":%d,"comm":"%s","cpu":1,"rss":1}`, i+1, strings.Repeat("é", 20)))
	}
	r.setState(r.devB, "system.metrics", `{"topCpu":[`+strings.Join(procs, ",")+`],"topRss":[`+strings.Join(procs, ",")+`]}`)
	_, _, body = r.get("/api/v1/devices/"+r.devB.String()+"/processes", adminToken)
	var got struct {
		CPU []telemetry.ProcView `json:"topCpu"`
		RSS []telemetry.ProcView `json:"topRss"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.CPU) != 10 || len(got.RSS) != 10 ||
		got.CPU[9].PID != 10 || got.CPU[0].Comm != strings.Repeat("é", 16) || got.RSS[9].Comm != strings.Repeat("é", 16) {
		t.Fatalf("%v %s", err, body)
	}
}

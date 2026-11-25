package telemetry_test

import (
	"os"
	"strings"
	"testing"
)

// the operator's notes on telemetry stay with the code they describe
func TestReadmeDescribesTelemetry(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/server/README.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	at := strings.Index(doc, "\n## Telemetry\n")
	if at < 0 {
		t.Fatal("no Telemetry section")
	}
	section := doc[at+1:]
	if next := strings.Index(section[3:], "\n## "); next >= 0 {
		section = section[:next+3]
	}
	for _, want := range []string{
		"logRetentionDays", "/api/v1/metrics", "authorization", "credentials_file", "scope `metrics`", "otlp-http",
		"telemetryExport", "cadence_otlp_export_dropped_total", "/api/v1/logs", "/processes", "log_line",
		"command line", "split across", "3 bytes", "sample_limit", "label_value_length_limit", "503 `timeout`",
		"/vitals", "`no_vitals`", "device_vitals", "device each 30 s", "last 10 minutes",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the Telemetry section does not mention %q", want)
		}
	}
}

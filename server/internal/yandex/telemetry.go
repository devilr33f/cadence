package yandex

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

const (
	maxTelemetryWire = 4 << 20
	maxTelemetryJSON = 16 << 20
	maxTelemetryRows = 4096
)

type LogSink interface {
	Insert(ctx context.Context, rows []telemetry.LogRow) error
}

type SecretLookup func(ctx context.Context, deviceID string) (map[string]string, error)

type vendorLogEntry struct {
	Timestamp int64
	Path      string
	Data      string
	DeviceID  string `json:"DeviceId"`
	Platform  string
}

var (
	authValuePattern   = regexp.MustCompile(`(?i)(authorization[[:space:]]*[:=][[:space:]]*(?:oauth|bearer)[[:space:]]+)[^[:space:],;]+`)
	jsonSecretPattern  = regexp.MustCompile(`(?i)(["']?(?:access_?token|oauth_?token|refresh_?token|auth_?token|token|secret|password)["']?[[:space:]]*[:=][[:space:]]*["']?)[^"',[:space:]}]+`)
	querySecretPattern = regexp.MustCompile(`(?i)([?&](?:access_?token|oauth_?token|token|code|password|secret|signature)=)[^&[:space:]]+`)
)

func (s *Service) captureLogs(ctx context.Context, req *http.Request) (int, error) {
	wire, err := readLimited(req.Body, maxTelemetryWire)
	if err != nil {
		return 0, err
	}
	body := wire
	if len(wire) >= 2 && wire[0] == 0x1f && wire[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(wire))
		if err != nil {
			return 0, err
		}
		body, err = readLimited(reader, maxTelemetryJSON)
		_ = reader.Close()
		if err != nil {
			return 0, err
		}
	} else if len(body) > maxTelemetryJSON {
		return 0, errBodyTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var entries []vendorLogEntry
	if err := decoder.Decode(&entries); err != nil {
		return 0, err
	}
	if err := ensureJSONEnd(decoder); err != nil || len(entries) > maxTelemetryRows {
		return 0, errBodyTooLarge
	}
	received := s.now()
	scrubbers := map[string]*telemetry.Scrubber{}
	rows := make([]telemetry.LogRow, 0, len(entries))
	for _, entry := range entries {
		if entry.Timestamp <= 0 || entry.Path == "" {
			return 0, errors.New("invalid vendor log entry")
		}
		vendorID := cleanVendorID(entry.DeviceID)
		cadenceID := s.devices[vendorID]
		scrub := scrubbers[cadenceID]
		if scrub == nil {
			scrub = &telemetry.Scrubber{}
			addRequestSecrets(scrub, req)
			if cadenceID != "" && s.secrets != nil {
				secrets, err := s.secrets(ctx, cadenceID)
				if err != nil {
					return 0, errors.Join(errTelemetryStore, err)
				}
				for name, value := range secrets {
					scrub.Set(name, value)
				}
			}
			scrubbers[cadenceID] = scrub
		}
		row := telemetry.LogRow{
			Time: time.Unix(entry.Timestamp, 0), Observed: received,
			Source: "yandex-" + cleanLogName(entry.Path), Severity: 9,
			Body: vendorPrefix(vendorID, entry.Platform) + scrubVendorText(entry.Data),
		}
		if cadenceID != "" {
			var id pgtype.UUID
			if err := id.Scan(cadenceID); err != nil {
				return 0, err
			}
			row.DeviceID = id
		}
		rows = append(rows, telemetry.NormalizeExternalLog(scrub, row))
	}
	if err := s.logs.Insert(ctx, rows); err != nil {
		return 0, errors.Join(errTelemetryStore, err)
	}
	return len(rows), nil
}

func readLimited(reader io.Reader, limit int) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, errBodyTooLarge
	}
	return body, nil
}

func addRequestSecrets(scrub *telemetry.Scrubber, req *http.Request) {
	for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Ya-Device-Token"} {
		if value := req.Header.Get(header); value != "" {
			scrub.Set(header, append([]string{value}, strings.Fields(value)...)...)
		}
	}
	for key, values := range req.URL.Query() {
		lower := strings.ToLower(key)
		if !strings.Contains(lower, "token") && !strings.Contains(lower, "secret") && !strings.Contains(lower, "password") &&
			!strings.Contains(lower, "signature") && lower != "code" && lower != "api_key" && lower != "api_key_128" {
			continue
		}
		for _, value := range values {
			scrub.Set("query", value)
		}
	}
}

func scrubVendorText(text string) string {
	text = authValuePattern.ReplaceAllString(text, `${1}«redacted:vendor_token»`)
	text = jsonSecretPattern.ReplaceAllString(text, `${1}«redacted:vendor_secret»`)
	return querySecretPattern.ReplaceAllString(text, `${1}«redacted:query_secret»`)
}

func cleanVendorID(value string) string {
	if len(value) == 0 || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if r != '-' && r != '_' && r != '.' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return value
}

func cleanLogName(path string) string {
	name := filepath.Base(path)
	if name == "." || name == "/" || len(name) > 64 {
		return "daemon"
	}
	for _, r := range name {
		if r != '-' && r != '_' && r != '.' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "daemon"
		}
	}
	return name
}

func vendorPrefix(deviceID, platform string) string {
	var prefix strings.Builder
	if deviceID != "" {
		prefix.WriteString("vendor_device=" + deviceID + " ")
	}
	platform = cleanVendorID(platform)
	if len(platform) > 32 {
		platform = ""
	}
	if platform != "" {
		prefix.WriteString("platform=" + platform + " ")
	}
	return prefix.String()
}

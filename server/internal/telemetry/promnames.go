package telemetry

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// a label value is cut to this many bytes, on a rune boundary
const maxLabelValue = 128

// the names the server's own metrics take: a device's series named into them
// is left out, so it can neither pass for the server's nor break its family
var reservedPrefixes = []string{"cadence_otlp_", "cadence_server_"}

// our labels: an attribute of one of these names is left out
var deviceLabels = []string{"device_id", "device_name", "board"}

func legal(r rune, first bool) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (!first && r >= '0' && r <= '9')
}

// promName is a device metric's Prometheus name: cadence_ and its name (a name
// of cadence's own, cadence., takes no second one), each character outside
// [A-Za-z0-9_] made _, a sum's ending in _total and a gauge's never (_gauge
// added), so a sum and a gauge never share a name.
func promName(name string, kind int16) string {
	var b strings.Builder
	b.WriteString("cadence_")
	for _, r := range strings.TrimPrefix(name, "cadence.") {
		if !legal(r, false) {
			r = '_'
		}
		b.WriteRune(r)
	}
	n := b.String()
	switch total := strings.HasSuffix(n, "_total"); {
	case kind == KindSum && !total:
		n += "_total"
	case kind != KindSum && total:
		n += "_gauge"
	}
	return n
}

func reservedName(name string) bool {
	return slices.ContainsFunc(reservedPrefixes, func(p string) bool { return strings.HasPrefix(name, p) })
}

// labelName is an attribute key as a label name: each character outside
// [A-Za-z0-9_] made _, _ before a leading digit; false for one Prometheus
// reserves (a leading __).
func labelName(key string) (string, bool) {
	var b strings.Builder
	for i, r := range key {
		switch {
		case legal(r, i == 0):
		case i == 0 && r >= '0' && r <= '9':
			b.WriteByte('_')
		default:
			r = '_'
		}
		b.WriteRune(r)
	}
	n := b.String()
	return n, n != "" && !strings.HasPrefix(n, "__")
}

func cutLabel(s string) string { return cutBytes(s, maxLabelValue) }

// cutBytes keeps at most n bytes of s, cut on a rune boundary
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// seriesLabels are a series' label names and values, ours first, then its
// attributes by key; key is the series' identity among them.
func seriesLabels(s Series) (names, values []string, key string) {
	names = slices.Clone(deviceLabels)
	values = []string{cutLabel(s.DeviceID), cutLabel(s.DeviceName), cutLabel(s.Board)}
	attrs := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(s.Attrs))
	dec.UseNumber()
	_ = dec.Decode(&attrs)
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		name, ok := labelName(k)
		if !ok || slices.Contains(names, name) {
			continue
		}
		var v string
		switch a := attrs[k].(type) {
		case string:
			v = a
		case bool:
			v = strconv.FormatBool(a)
		case json.Number:
			v = a.String()
		default:
			continue
		}
		names, values = append(names, name), append(values, cutLabel(v))
	}
	order := make([]int, len(names))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return strings.Compare(names[a], names[b]) })
	var kb strings.Builder
	for _, i := range order {
		kb.WriteString(names[i])
		kb.WriteByte(0)
		kb.WriteString(values[i])
		kb.WriteByte(0)
	}
	return names, values, kb.String()
}

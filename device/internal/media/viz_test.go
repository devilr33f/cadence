package media

import (
	"encoding/json"
	"slices"
	"testing"
)

// both schemas name exactly the styles Apply takes, so the dashboard never offers one the device refuses
func TestVizSchemasNameEveryStyle(t *testing.T) {
	var want []string
	for s := range vizStyles {
		want = append(want, s)
	}
	slices.Sort(want)
	for _, schema := range []string{vizConfigSchema, vizStateSchema} {
		var doc struct {
			Properties struct {
				Style struct {
					Enum []string `json:"enum"`
				} `json:"style"`
			} `json:"properties"`
		}
		if err := json.Unmarshal([]byte(schema), &doc); err != nil {
			t.Fatal(err)
		}
		got := slices.Sorted(slices.Values(doc.Properties.Style.Enum))
		if !slices.Equal(got, want) {
			t.Fatalf("schema styles %v, Apply takes %v", got, want)
		}
	}
	for _, s := range []string{"bands", "pulse", "spectrum", "equalizer", "orbit", "beat"} {
		if !vizStyles[s] {
			t.Fatalf("style %q refused", s)
		}
	}
}

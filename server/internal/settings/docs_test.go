package settings

import (
	"strings"
	"testing"
)

// the scope notes name every key the code refuses for the scope
func TestScopeNotesNameRefusedKeys(t *testing.T) {
	for id, keys := range map[string][]string{"put-global-settings": deviceOnly, "put-device-settings": globalOnly} {
		for _, k := range keys {
			if !strings.Contains(docs[id].BodyNote, k) {
				t.Errorf("%s note lacks %s", id, k)
			}
		}
	}
}

// the global put replaces the whole set (ruling M7bs-R5), and both puts name
// every key that names a provider instance
func TestPutNotesSayWhatIsReplacedAndChecked(t *testing.T) {
	if note := docs["put-global-settings"].BodyNote; !strings.Contains(note, "replaces the whole global set") ||
		!strings.Contains(docs["put-global-settings"].Summary, "whole") {
		t.Errorf("global put: %s", note)
	}
	for _, id := range []string{"put-global-settings", "put-device-settings"} {
		for _, k := range InstanceKeys {
			if !strings.Contains(docs[id].BodyNote, k) {
				t.Errorf("%s note lacks %s", id, k)
			}
		}
	}
}

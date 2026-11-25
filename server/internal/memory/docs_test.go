package memory

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func tagOf(t *testing.T, v any, field, tag string) string {
	t.Helper()
	f, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		t.Fatalf("no field %s", field)
	}
	return f.Tag.Get(tag)
}

// the documented enums and limits are the code's own
func TestDocsMatchCode(t *testing.T) {
	kinds := strings.Join([]string{KindPreference, KindFact, KindAlias, KindLesson}, ",")
	scopes := strings.Join([]string{ScopeGlobal, ScopeDevice}, ",")
	for _, v := range []any{CreateMemoryRequest{}, EditMemoryRequest{}} {
		if got := tagOf(t, v, "Kind", "enum"); got != kinds {
			t.Errorf("%T kinds %q, want %q", v, got, kinds)
		}
		if got := tagOf(t, v, "Scope", "enum"); got != scopes {
			t.Errorf("%T scopes %q, want %q", v, got, scopes)
		}
		if got := tagOf(t, v, "Text", "maxLength"); got != strconv.Itoa(maxTextChars) {
			t.Errorf("%T text max %q, want %d", v, got, maxTextChars)
		}
	}
	for _, id := range []string{"list-memories", "list-device-compactions", "list-device-learning"} {
		note := ""
		for _, q := range docs[id].Query {
			if q.Name == "limit" {
				note = q.Note
			}
		}
		if !strings.Contains(note, "at most "+strconv.Itoa(maxPage)) {
			t.Errorf("%s limit note %q lacks max %d", id, note, maxPage)
		}
	}
}

package tasks

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func fieldTag(t *testing.T, v any, field, name string) string {
	t.Helper()
	f, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		t.Fatalf("no field %s", field)
	}
	return f.Tag.Get(name)
}

// the documented enums and limits are the code's own
func TestDocsMatchCode(t *testing.T) {
	if got := fieldTag(t, ApproveRequest{}, "Decision", "enum"); got != DecisionOnce+","+DecisionDeny {
		t.Errorf("decisions %q", got)
	}
	if got := fieldTag(t, ReplyRequest{}, "Message", "maxLength"); got != strconv.Itoa(maxMessage) {
		t.Errorf("message max %q, want %d", got, maxMessage)
	}
	for _, s := range []string{StatusQueued, StatusRunning, StatusWaitingInput, StatusDone, StatusFailed, StatusCancelled, StatusUnknown} {
		if !strings.Contains(strings.Join(statuses, ","), s) {
			t.Errorf("status %s undocumented", s)
		}
	}
	for _, q := range docs["list-tasks"].Query {
		if q.Name == "limit" && *q.Schema.Maximum != float64(maxPage) {
			t.Errorf("limit max %v, want %d", *q.Schema.Maximum, maxPage)
		}
	}
}

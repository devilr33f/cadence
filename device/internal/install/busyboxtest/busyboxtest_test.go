package busyboxtest

import "testing"

// without a docker cli the tests skip on a developer's machine and fail in ci, where skipping would hide them
func TestMissingDocker(t *testing.T) {
	for _, tc := range []struct {
		ci   string
		want string
	}{{"", "skip"}, {"true", "fail"}} {
		got := missing(tc.ci)
		if got != tc.want {
			t.Errorf("CI=%q: %s, want %s", tc.ci, got, tc.want)
		}
	}
}

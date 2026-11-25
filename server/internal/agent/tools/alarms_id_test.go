package tools

import (
	"regexp"
	"testing"
)

// an id is never one the device keeps already, which a set would replace in place: a draw the device holds is drawn
// again
func TestNewIDSkipsAnIDTheDeviceKeeps(t *testing.T) {
	var drawn []string
	id := newID(func(id string) bool {
		drawn = append(drawn, id)
		return len(drawn) == 1
	})
	valid := regexp.MustCompile(`^[a-z0-9]{8}$`)
	if len(drawn) != 2 || id != drawn[1] || !valid.MatchString(id) || !valid.MatchString(drawn[0]) {
		t.Fatalf("id %q from draws %q", id, drawn)
	}
}

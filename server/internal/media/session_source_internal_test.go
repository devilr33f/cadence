package media

import (
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// every source the protocol names has its name here, so a new one is never shown as unspecified
func TestEverySourceHasAName(t *testing.T) {
	for n := range cadencev2.MediaSource_name {
		s := cadencev2.MediaSource(n)
		if got := sourceName(s); (got == unspecified) != (s == cadencev2.MediaSource_MEDIA_SOURCE_UNSPECIFIED) {
			t.Fatalf("%v: %q", s, got)
		}
	}
	if got := sourceName(cadencev2.MediaSource_MEDIA_SOURCE_BLUETOOTH); got != "bluetooth" {
		t.Fatalf("bluetooth: %q", got)
	}
}

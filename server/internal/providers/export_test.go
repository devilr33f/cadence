package providers

import (
	"testing"
	"time"
)

// SetNow sets the test limiter's clock for one test.
func SetNow(t *testing.T, f func() time.Time) {
	t.Helper()
	now = f
	t.Cleanup(func() { now = time.Now })
}

package deepgram

import "testing"

// SetDecodeHook installs a hook for one test.
func SetDecodeHook(t *testing.T, h func([]byte)) {
	t.Helper()
	decodeHook = h
	t.Cleanup(func() { decodeHook = nil })
}

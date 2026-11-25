//go:build !linux

package midi

import "errors"

// openFramebuffer has no framebuffer to open off linux
func openFramebuffer(string) (framebuffer, error) { return nil, errors.ErrUnsupported }

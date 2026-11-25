//go:build !unix

package discovery

import (
	"context"
	"errors"
)

var errUnsupported = errors.New("discovery: unsupported on this system")

// devices run linux; elsewhere (cadence-install on windows) only browsing works
func listen(context.Context, int) (transport, error) { return nil, errUnsupported }

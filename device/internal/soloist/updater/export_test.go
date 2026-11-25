package updater

import (
	"context"
	"io"
	"net/netip"
)

func Extract(body io.Reader, dest string) error { return extract(body, dest, defaultLimits) }

func ExtractLimited(body io.Reader, dest string, maxBinary, maxInflated int64) error {
	return extract(body, dest, limits{binary: maxBinary, inflated: maxInflated})
}

func InstallSteps(dir string) []func() error { return installSteps(layout(dir)) }

func SetLookup(c *CDN, f func(ctx context.Context, host string) ([]netip.Addr, error)) { c.lookup = f }

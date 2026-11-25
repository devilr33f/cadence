package memory

import "context"

var (
	Oversize    = oversize
	LooksSecret = looksSecret
)

// Sweep runs one pass of the idle trigger and returns the devices whose job started.
func (c *Compactor) Sweep(ctx context.Context) []string { return c.sweep(ctx) }

// Wait waits for the running jobs.
func (c *Compactor) Wait() { c.wg.Wait() }

// CompactNow runs a job for the device as a trigger would and waits for it;
// false when none started.
func (c *Compactor) CompactNow(deviceID string) bool {
	ok := c.start(deviceID, func(ctx context.Context) { _ = c.job(ctx, deviceID, "test") })
	c.wg.Wait()
	return ok
}

var Untouchable = untouchable

// Wait waits for the embeddings the admin has started.
func (a *Admin) Wait() { a.wg.Wait() }

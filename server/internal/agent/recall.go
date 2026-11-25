package agent

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/memory"
)

// Memories picks the memories a turn reads. It must not fail: whatever goes
// wrong narrows what it returns.
type Memories interface {
	Recall(ctx context.Context, q memory.Query) memory.Recalled
}

// startRecall recalls beside the history read, so neither waits on the
// other. The returned func waits for the answer, which the bound caps; a
// turn without a Memories dependency gets nothing and no event.
func (r *Runner) startRecall(ctx context.Context, tr *turn) func() (memory.Recalled, bool) {
	if r.d.Memories == nil {
		return func() (memory.Recalled, bool) { return memory.Recalled{}, false }
	}
	rctx, cancel := context.WithTimeout(ctx, r.lim.recall)
	done := make(chan memory.Recalled, 1)
	q := memory.Query{DeviceID: tr.DeviceID, Input: tr.Text, Embed: tr.v.Embed, Scopes: tr.v.MemoryScopes}
	go func() {
		defer func() {
			// the value can carry memory text, so it is dropped
			if recover() != nil {
				done <- memory.Recalled{Skipped: memory.SkipError}
			}
		}()
		done <- r.d.Memories.Recall(rctx, q)
	}()
	return func() (memory.Recalled, bool) {
		defer cancel()
		select {
		case res := <-done:
			return res, true
		case <-rctx.Done():
		}
		select {
		case res := <-done:
			return res, true
		default:
		}
		if ctx.Err() != nil {
			return memory.Recalled{Skipped: memory.SkipCanceled}, true
		}
		return memory.Recalled{Skipped: memory.SkipTimeout}, true
	}
}

// collectRecall waits for the recall, publishes what it found (counts only,
// never the text) and returns the memories to show: the pinned ones and those
// the input recalled, and the recall itself (its input's vector).
func (r *Runner) collectRecall(tr *turn, wait func() (memory.Recalled, bool)) (pinned, recalled []memory.Memory, res memory.Recalled) {
	res, ok := wait()
	if !ok {
		return nil, nil, memory.Recalled{}
	}
	data := map[string]any{"count": len(res.Memories), "pinned": res.Pinned}
	if res.Skipped != "" {
		data["skipped_reason"] = res.Skipped
	}
	r.publish(tr, "memory_recall", data)
	n := min(max(res.Pinned, 0), len(res.Memories))
	return res.Memories[:n], res.Memories[n:], res
}

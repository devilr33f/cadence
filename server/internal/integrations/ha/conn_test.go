package ha

import (
	"context"
	"encoding/json"
	"testing"
)

// a result that arrived as the deadline passed wins over the timeout
func TestResultReadyAtTheDeadlineWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &conn{pending: map[int64]chan inbound{}, subs: map[int64]func(json.RawMessage){}}
	for range 200 {
		ch := make(chan inbound, 1)
		ch <- inbound{ID: 1, Type: typeResult}
		m, err := c.wait(ctx, 1, ch)
		if err != nil || m.Type != typeResult {
			t.Fatalf("got %+v %v", m, err)
		}
	}
}

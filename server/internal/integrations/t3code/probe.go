package t3code

import (
	"context"
	"encoding/json"
	"net/http"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// probe reads the HTTP shell snapshot (GET /api/orchestration/shell with the
// protocol header and the bearer): it needs orchestration:read and changes
// nothing. 401/403 is auth, a server without protocol 2 (an answer that is no
// shell snapshot) bad_response.
func probe(ctx context.Context, v any) error {
	c, ok := v.(*Client)
	if !ok {
		return providers.ProbeBadResponse
	}
	b, err := providers.ProbeGET(ctx, c.http, c.ep.http+"/api/orchestration/shell", http.Header{
		"Authorization": {"Bearer " + c.token},
		ProtocolHeader:  {ProtocolVersion},
	})
	if err != nil {
		return err
	}
	var snap struct {
		SnapshotSequence *int64           `json:"snapshotSequence"`
		Threads          *json.RawMessage `json:"threads"`
		Projects         *json.RawMessage `json:"projects"`
	}
	if json.Unmarshal(b, &snap) != nil || snap.SnapshotSequence == nil || snap.Threads == nil || snap.Projects == nil {
		return providers.ProbeBadResponse
	}
	return nil
}

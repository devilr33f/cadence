package conversation

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// AppendRaw bypasses encode so a test can make one row fail at the database.
func (s *Store) AppendRaw(ctx context.Context, deviceID, turnID string, contents [][]byte) error {
	var dev pgtype.UUID
	if err := dev.Scan(deviceID); err != nil {
		return err
	}
	p := db.AppendConversationTurnParams{DeviceID: dev, TurnID: turnID}
	for i, c := range contents {
		p.Seqs = append(p.Seqs, int32(i))
		p.Roles = append(p.Roles, "user")
		p.Contents = append(p.Contents, c)
	}
	return s.insert(ctx, p)
}

-- +goose Up
-- the current conversation starts after this message; older turns stay stored
-- for the history, closed-segment compactions and memory learning (D8)
ALTER TABLE devices ADD COLUMN conversation_after_id bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE devices DROP COLUMN conversation_after_id;

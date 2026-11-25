-- +goose Up
-- a failed turn's codes-only outcome beside its messages: the stage and step,
-- provider/model ids, an upstream http status and the wait/limit. no upstream
-- error body is stored. null for a successful turn or one stored before this.
ALTER TABLE conversation_turns ADD COLUMN failure jsonb;

-- +goose Down
ALTER TABLE conversation_turns DROP COLUMN failure;

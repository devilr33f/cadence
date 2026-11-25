-- +goose Up
-- an answered event keeps who answered a task's approval and how: the voice
-- session's device and the user's own words, or the dashboard
ALTER TABLE task_events DROP CONSTRAINT task_events_kind_check;
ALTER TABLE task_events ADD CONSTRAINT task_events_kind_check
    CHECK (kind IN ('progress', 'waiting_input', 'done', 'failed', 'cancelled', 'unknown', 'answered'));
ALTER TABLE task_events ADD COLUMN answer jsonb
    CHECK ((kind = 'answered') = (answer IS NOT NULL) AND (answer IS NULL OR jsonb_typeof(answer) = 'object'));

-- +goose Down
DELETE FROM task_events WHERE kind = 'answered';
ALTER TABLE task_events DROP COLUMN answer;
ALTER TABLE task_events DROP CONSTRAINT task_events_kind_check;
ALTER TABLE task_events ADD CONSTRAINT task_events_kind_check
    CHECK (kind IN ('progress', 'waiting_input', 'done', 'failed', 'cancelled', 'unknown'));

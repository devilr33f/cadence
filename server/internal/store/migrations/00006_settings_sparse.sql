-- +goose Up
UPDATE settings SET data = COALESCE(
    (SELECT jsonb_object_agg(key, value) FROM jsonb_each(data) WHERE value <> '""'::jsonb),
    '{}'::jsonb);

-- +goose Down
SELECT 1;

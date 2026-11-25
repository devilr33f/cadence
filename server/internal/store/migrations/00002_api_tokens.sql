-- +goose Up
CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    hash         bytea       NOT NULL UNIQUE,
    scope        text        NOT NULL DEFAULT 'admin',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);

-- +goose Down
DROP TABLE api_tokens;

-- +goose Up
-- how long the mode's model may stay silent before its first token, in ms; 0
-- keeps the built-in 10 s (slow reasoning models behind a gateway need more)
ALTER TABLE agent_modes ADD COLUMN first_token_ms int NOT NULL DEFAULT 0
    CHECK (first_token_ms = 0 OR first_token_ms BETWEEN 2000 AND 120000);

-- +goose Down
ALTER TABLE agent_modes DROP COLUMN first_token_ms;

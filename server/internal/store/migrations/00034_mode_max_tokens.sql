-- +goose Up
-- the mode's own ceiling on a step's max_tokens; 0 keeps the built-in default
-- (4096 with reasoning on, else 1024)
ALTER TABLE agent_modes ADD COLUMN max_tokens int NOT NULL DEFAULT 0
    CHECK (max_tokens = 0 OR max_tokens BETWEEN 64 AND 32768);

-- +goose Down
ALTER TABLE agent_modes DROP COLUMN max_tokens;

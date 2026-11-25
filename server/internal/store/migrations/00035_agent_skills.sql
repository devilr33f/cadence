-- +goose Up
-- the owner's skills: instruction packs a turn loads when its input calls for
-- them. A row named as a built-in skill replaces it (disabled: switches it
-- off); modes NULL is every mode.
CREATE TABLE agent_skills (
    name        text        PRIMARY KEY CHECK (name ~ '^[a-z][a-z0-9_-]{0,47}$'),
    description text        NOT NULL CHECK (char_length(description) BETWEEN 1 AND 200),
    body        text        NOT NULL CHECK (char_length(body) BETWEEN 1 AND 4000),
    tools       text[]      NOT NULL DEFAULT '{}',
    triggers    text[]      NOT NULL DEFAULT '{}',
    modes       text[],
    enabled     boolean     NOT NULL DEFAULT true,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- the skills a turn loaded, by name; NULL for a turn stored before or that
-- loaded none
ALTER TABLE conversation_turns ADD COLUMN skills text[];

-- +goose Down
ALTER TABLE conversation_turns DROP COLUMN skills;
DROP TABLE agent_skills;

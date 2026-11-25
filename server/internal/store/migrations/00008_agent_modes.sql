-- +goose Up
-- model '' uses the device's model setting; tools NULL offers every tool
CREATE TABLE agent_modes (
    name        text  PRIMARY KEY,
    model       text  NOT NULL DEFAULT '',
    reasoning   text  NOT NULL CHECK (reasoning IN ('none', 'low', 'medium', 'high')),
    max_steps   int   NOT NULL CHECK (max_steps > 0),
    max_seconds int   NOT NULL CHECK (max_seconds > 0),
    interim     text  NOT NULL CHECK (interim IN ('drop', 'speak')),
    tools       text[],
    aliases     jsonb NOT NULL DEFAULT '{}'
);

INSERT INTO agent_modes (name, model, reasoning, max_steps, max_seconds, interim, aliases) VALUES
    ('fast', 'deepseek/deepseek-v4.1-flash-1017', 'none', 8, 20, 'drop',
     '{"en": ["fast", "quick"], "ru": ["быстрый"]}'),
    ('smart', '', 'medium', 25, 90, 'speak',
     '{"en": ["smart", "clever"], "ru": ["умный"]}')
ON CONFLICT (name) DO NOTHING;

-- +goose Down
DROP TABLE agent_modes;

-- +goose Up
-- busy signals (busy mode): while an unexpired signal covers a device, the
-- server holds unasked speech there. a source keeps one signal; devices null
-- covers every device. every signal expires, at most a day after its write,
-- so a source that stops sending lets the device speak again.
CREATE TABLE busy_signals (
    source     text        PRIMARY KEY,
    reason     text        NOT NULL CHECK (reason IN ('call', 'screenshare', 'meeting', 'dnd')),
    devices    uuid[],
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (devices IS NULL OR cardinality(devices) > 0),
    CHECK (expires_at <= updated_at + interval '24 hours')
);

-- the presence scope: a token that sets and clears busy signals only
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_scopes_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_scopes_check
    CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['admin', 'ha', 'metrics', 'presence']::text[]);

-- +goose Down
-- a token left with no scope goes rather than become a wider one
UPDATE api_tokens SET scopes = array_remove(scopes, 'presence');
DELETE FROM api_tokens WHERE cardinality(scopes) = 0;
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_scopes_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_scopes_check
    CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['admin', 'ha', 'metrics']::text[]);
DROP TABLE busy_signals;

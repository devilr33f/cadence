-- +goose Up
-- scoped api tokens (M7a-D6): a token holds the scopes it may use, at least
-- one of the known ones; every token made before scopes is an admin token
ALTER TABLE api_tokens ADD COLUMN scopes text[] NOT NULL DEFAULT '{admin}'
    CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['admin', 'ha', 'metrics']::text[]);
ALTER TABLE api_tokens DROP COLUMN scope;

-- names become unique: of a name used twice, the later token keeps it with its
-- id's first 8 characters appended
UPDATE api_tokens t SET name = t.name || '-' || left(t.id::text, 8)
WHERE EXISTS (SELECT 1 FROM api_tokens o WHERE o.name = t.name AND (o.created_at, o.id) < (t.created_at, t.id));
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_name_key UNIQUE (name);

-- a known browser's failed logins over its whole life: the fifth forgets it,
-- and a success replaces it with a fresh one
ALTER TABLE admin_browsers ADD COLUMN failures integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE admin_browsers DROP COLUMN failures;
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_name_key;
-- the old column knows no scope but admin: a narrower token goes rather than
-- become an admin one
DELETE FROM api_tokens WHERE NOT ('admin' = ANY (scopes));
ALTER TABLE api_tokens ADD COLUMN scope text NOT NULL DEFAULT 'admin';
ALTER TABLE api_tokens DROP COLUMN scopes;

-- +goose Up
-- the catalog's remembered choices per title (the source of a title group,
-- and the voiceover, quality or file of a provider that keeps no prefs of
-- its own): title is the SHA-256 of the household and the provider's key,
-- so no provider id is stored in clear; selection is the choice per group
-- kind. torrplayer's titles live in torrplayer's own prefs instead.
CREATE TABLE catalog_choices (
    title      bytea       PRIMARY KEY CHECK (octet_length(title) = 32),
    selection  jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE catalog_choices;

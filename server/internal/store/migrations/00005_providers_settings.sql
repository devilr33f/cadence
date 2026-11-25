-- +goose Up
CREATE TABLE provider_instances (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL UNIQUE,
    module     text        NOT NULL,
    config     jsonb       NOT NULL DEFAULT '{}',
    secrets    bytea,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settings (
    scope text  PRIMARY KEY,
    data  jsonb NOT NULL
);

-- +goose Down
DROP TABLE settings;
DROP TABLE provider_instances;

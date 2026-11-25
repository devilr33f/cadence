-- +goose Up
CREATE TABLE device_capabilities (
    device_id     uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    capability    text        NOT NULL,
    version       int         NOT NULL,
    descriptor    jsonb       NOT NULL,
    desired       jsonb,
    desired_gen   bigint      NOT NULL DEFAULT 0,
    applied_gen   bigint      NOT NULL DEFAULT 0,
    applied_error text        NOT NULL DEFAULT '',
    state         jsonb,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, capability)
);

-- +goose Down
DROP TABLE device_capabilities;

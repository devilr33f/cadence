-- +goose Up
CREATE TABLE device_capability_secrets (
    device_id  uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    capability text        NOT NULL,
    name       text        NOT NULL,
    sealed     bytea       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, capability, name)
);

-- +goose Down
DROP TABLE device_capability_secrets;

-- +goose Up
CREATE TABLE devices (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text        NOT NULL,
    model             text        NOT NULL,
    status            text        NOT NULL CHECK (status IN ('pending', 'enrolled')),
    token_hash        bytea UNIQUE,
    capabilities_hash bytea,
    created_at        timestamptz NOT NULL DEFAULT now(),
    enrolled_at       timestamptz,
    last_seen_at      timestamptz
);

CREATE TABLE enroll_codes (
    code_hash  bytea PRIMARY KEY,
    device_id  uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    model      text        NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

-- +goose Down
DROP TABLE enroll_codes;
DROP TABLE devices;

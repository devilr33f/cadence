-- +goose Up
-- the owner's dashboard login (M7a-D4): one row, the password's argon2id hash
-- as a PHC string; `admin password set` writes it and revokes every session
-- and known browser
CREATE TABLE admin_users (
    id            smallint    PRIMARY KEY CHECK (id = 1),
    password_hash text        NOT NULL,
    updated_at    timestamptz NOT NULL
);

-- a session cookie's token is stored as its sha256 only; a session ends at
-- expires_at (30 days), after 7 idle days or when revoked, and such rows are
-- pruned daily
CREATE TABLE admin_sessions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash   bytea       NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz
);

-- a known browser (Ruling M7a-R2): a login's second cookie, stored as its
-- sha256 only, that keeps the owner's logins out of the per-ip and global
-- limits; it ends after a year, five failed logins in 15 minutes, the next
-- login (which replaces it) or a password change
CREATE TABLE admin_browsers (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash bytea       NOT NULL UNIQUE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

-- +goose Down
DROP TABLE admin_browsers;
DROP TABLE admin_sessions;
DROP TABLE admin_users;

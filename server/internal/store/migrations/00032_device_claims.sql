-- +goose Up
-- device-first enrolment (claims, tv plan W0-C item 8, design §22): a screen
-- opens a claim and shows its 6-character code; the owner approves it in the
-- dashboard with a name and a room. the claim's id is the screen's bearer for
-- its polls, so only its hash is kept. approve creates the device pending, as
-- create-device does, and the screen's next poll enrols it: the token is made
-- there and never stored in the clear. state expired is pending past its
-- expiry; a row is pruned an hour after it ended. device_id goes null when
-- the device is deleted before the screen took its token (state removed)
CREATE TABLE device_claims (
    id_hash     bytea       PRIMARY KEY,
    code        text        NOT NULL CHECK (code ~ '^[2-9A-HJ-NP-Z]{6}$'),
    board       text        NOT NULL,
    source      text        NOT NULL DEFAULT '' CHECK (char_length(source) <= 64),
    state       text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'declined', 'done')),
    device_id   uuid        REFERENCES devices ON DELETE SET NULL,
    created_at  timestamptz NOT NULL,
    expires_at  timestamptz NOT NULL,
    finished_at timestamptz
);

-- an open code is one claim's: a new one never takes a code still shown
CREATE UNIQUE INDEX device_claims_open_code ON device_claims (code) WHERE state = 'pending';
CREATE INDEX device_claims_expires ON device_claims (expires_at);

-- +goose Down
DROP TABLE device_claims;

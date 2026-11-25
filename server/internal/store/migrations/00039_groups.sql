-- +goose Up
-- multiroom playback groups: a leader's audiod streams the group's audio to
-- its members (media.group). the key authenticates the group's clock and
-- stream, sealed with the master key (aad group-key:<id>) and generated again
-- whenever the membership changes
CREATE TABLE groups (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name             text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    kind             text        NOT NULL CHECK (kind IN ('group', 'stereo_pair')),
    preferred_leader uuid        REFERENCES devices ON DELETE SET NULL,
    latency_ms       int         NOT NULL DEFAULT 1000 CHECK (latency_ms BETWEEN 200 AND 5000),
    bitrate_kbps     int         NOT NULL DEFAULT 256 CHECK (bitrate_kbps BETWEEN 64 AND 510),
    pcm              boolean     NOT NULL DEFAULT false,
    wait_ms          int         NOT NULL DEFAULT 3000 CHECK (wait_ms BETWEEN 0 AND 10000),
    key_sealed       bytea       NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- a group is named by voice: no two alike in case
CREATE UNIQUE INDEX groups_name_lower ON groups (lower(name));

-- a device is in one group at most
CREATE TABLE group_members (
    group_id  uuid NOT NULL REFERENCES groups ON DELETE CASCADE,
    device_id uuid NOT NULL UNIQUE REFERENCES devices ON DELETE CASCADE,
    channel   text NOT NULL DEFAULT 'both' CHECK (channel IN ('both', 'left', 'right', 'mono')),
    trim_ms   int  NOT NULL DEFAULT 0 CHECK (trim_ms BETWEEN -500 AND 500),
    position  int  NOT NULL,
    PRIMARY KEY (group_id, device_id)
);

-- +goose Down
DROP TABLE group_members;
DROP TABLE groups;

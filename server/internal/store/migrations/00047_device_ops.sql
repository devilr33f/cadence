-- +goose Up
-- the device-update store: build artifacts (a tarball or apk per board, its files' hashes parsed from the
-- tarball's MANIFEST), the runs that install them (a resolved opsplan and its steps' results), and each
-- device's out-of-band access (adb over the lan, ssh to the pi) the server-driven installs go through.
-- agents never trigger a run (rule 7): every row comes from the owner's dashboard or admin token, and the
-- stored plan is one the server built from a board profile, never free-form shell
CREATE TABLE device_artifacts (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    board      text        NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('tar', 'apk', 'deb')),
    -- the build the artifact carries: the MANIFEST's cadence header, or the apk's version for the owner to name
    version    text        NOT NULL,
    label      text        NOT NULL DEFAULT '',
    sha256     text        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size       bigint      NOT NULL CHECK (size > 0),
    data       bytea       NOT NULL,
    -- [{path, sha256, size, mode}]: a tar's MANIFEST lines with the tar entry's size and mode, or the one file an apk is
    files      jsonb       NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX device_artifacts_board ON device_artifacts (board, created_at DESC);

CREATE TABLE ops_runs (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id      uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    -- null when the device row was deleted with the artifact left: the run's own plan keeps what it installed
    artifact_id    uuid        REFERENCES device_artifacts ON DELETE SET NULL,
    board          text        NOT NULL,
    target_version text        NOT NULL,
    -- link (the daemon runs the plan itself), adb or ssh (the server does)
    transport      text        NOT NULL CHECK (transport IN ('link', 'adb', 'ssh')),
    plan           jsonb       NOT NULL,
    -- running | awaiting_restart | succeeded | failed | rolled_back | needs_attention
    status         text        NOT NULL DEFAULT 'running',
    error_code     text        NOT NULL DEFAULT '',
    -- [opsplan.StepResult], in step order
    steps          jsonb       NOT NULL DEFAULT '[]',
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);

CREATE INDEX ops_runs_device ON ops_runs (device_id, started_at DESC);

-- one run at a time per device
CREATE UNIQUE INDEX ops_runs_one_active ON ops_runs (device_id) WHERE status IN ('running', 'awaiting_restart');

CREATE TABLE device_access (
    device_id     uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    -- adb: host:port (the lan address adbd listens on), ssh: user@host:port
    kind          text        NOT NULL CHECK (kind IN ('adb', 'ssh')),
    addr          text        NOT NULL,
    -- the ssh private key, sealed with the master key; empty for adb
    ssh_key       text        NOT NULL DEFAULT '',
    -- the host key pinned on the first connection (ssh alone); a change refuses the transport
    ssh_host_key  text        NOT NULL DEFAULT '',
    last_probe    timestamptz,
    last_ok       boolean,
    PRIMARY KEY (device_id, kind)
);

-- the daemon build each device last linked with (hello.daemon_version), so a run's success is the relink
-- that names its target version
ALTER TABLE devices ADD COLUMN daemon_version text NOT NULL DEFAULT '';

-- +goose Down
DROP TABLE device_access;
DROP TABLE ops_runs;
DROP TABLE device_artifacts;
ALTER TABLE devices DROP COLUMN daemon_version;

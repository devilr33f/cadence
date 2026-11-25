-- +goose Up
-- a calibration belongs to the output that sounded during the run, not to whatever route the device uses later.
-- group_members.trim_us stays the fallback for an output never calibrated; existing rows move nowhere
CREATE TABLE group_output_trims (
    group_id  uuid NOT NULL,
    device_id uuid NOT NULL,
    output_id text NOT NULL CHECK (char_length(output_id) BETWEEN 1 AND 64),
    trim_us   int  NOT NULL CHECK (trim_us BETWEEN -500000 AND 500000),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, device_id, output_id),
    FOREIGN KEY (group_id, device_id) REFERENCES group_members (group_id, device_id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE group_output_trims;

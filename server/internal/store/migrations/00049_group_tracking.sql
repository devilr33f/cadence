-- +goose Up
-- ultrasonic live tracking is opt-in per group. an output's latest applied calibration says whether its ultrasound
-- reached a microphone; only those active outputs join the leader's tracking plan
ALTER TABLE groups ADD COLUMN tracking_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE group_output_trims ADD COLUMN ultrasonic text NOT NULL DEFAULT ''
    CHECK (ultrasonic IN ('', 'ok', 'weak', 'missed', 'unsupported'));

-- +goose Down
ALTER TABLE group_output_trims DROP COLUMN ultrasonic;
ALTER TABLE groups DROP COLUMN tracking_enabled;

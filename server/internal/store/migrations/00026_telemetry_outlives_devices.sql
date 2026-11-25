-- +goose Up
-- a deleted device's log lines and metric points stay until their retention
-- (M7bs-D4): a cascade would delete them from every day partition of
-- device_logs, inside the delete's transaction
ALTER TABLE device_logs DROP CONSTRAINT device_logs_device_id_fkey;
ALTER TABLE metric_samples DROP CONSTRAINT metric_samples_device_id_fkey;

-- +goose Down
DELETE FROM device_logs l WHERE l.device_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM devices d WHERE d.id = l.device_id);
DELETE FROM metric_samples s WHERE NOT EXISTS (SELECT 1 FROM devices d WHERE d.id = s.device_id);
ALTER TABLE device_logs ADD CONSTRAINT device_logs_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices ON DELETE CASCADE;
ALTER TABLE metric_samples ADD CONSTRAINT metric_samples_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices ON DELETE CASCADE;

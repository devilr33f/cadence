-- +goose Up
-- the device owns its volume since M4c: audio.output sets the level by command
-- and its config takes none. the device still accepts a legacy {"level": n} to
-- seed a level it does not have yet, so a pending stored level would be pushed
-- and could seed it from a stale server value; it goes. the generation stays:
-- {} changes nothing on the device
UPDATE device_capabilities SET desired = desired - 'level', updated_at = now()
WHERE capability = 'audio.output' AND desired ? 'level';

-- +goose Down
SELECT 1;

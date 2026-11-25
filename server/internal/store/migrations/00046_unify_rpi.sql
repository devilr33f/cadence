-- +goose Up
-- one raspberry pi board carries the screen and bluetooth outputs. keep the device rows (and their tokens, groups and
-- settings): only the model name changes
UPDATE devices SET model = 'rpi' WHERE model IN ('rpi-bt', 'rpi-tv');

-- +goose Down
-- rpi-bt was the only rpi model with a shipping deployment before the unified board
UPDATE devices SET model = 'rpi-bt' WHERE model = 'rpi';

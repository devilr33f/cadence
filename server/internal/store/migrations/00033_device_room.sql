-- +goose Up
-- the room a device is in (tv plan W0-C item 9a): the owner's name for it, set
-- at a claim's approval or in the dashboard, '' for none. a speaker and a
-- screen of one room are one assistant. a device that named a home assistant
-- area (its haArea setting, the rooms directory until now) starts in a room of
-- that name
ALTER TABLE devices ADD COLUMN room text NOT NULL DEFAULT '' CHECK (char_length(room) <= 64);

UPDATE devices d SET room = s.data->>'haArea'
FROM settings s
WHERE s.scope = d.id::text AND jsonb_typeof(s.data->'haArea') = 'string' AND char_length(s.data->>'haArea') <= 64;

-- +goose Down
ALTER TABLE devices DROP COLUMN room;

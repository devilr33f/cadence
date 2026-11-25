-- +goose Up
-- a group with more than one screen opens a video started on one of them on the others as followers (multiroom P4):
-- muted, their picture slaved to the group's stream. on by default, for the groups made before too
ALTER TABLE groups ADD COLUMN follow_video boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE groups DROP COLUMN follow_video;

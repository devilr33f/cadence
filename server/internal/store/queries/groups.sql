-- name: CreateGroup :exec
INSERT INTO groups (id, name, kind, preferred_leader, latency_ms, bitrate_kbps, pcm, wait_ms, key_sealed, auto_latency,
    auto_bitrate, auto_wait, follow_video, tracking_enabled)
VALUES (@id, @name, @kind, @preferred_leader, @latency_ms, @bitrate_kbps, @pcm, @wait_ms, @key_sealed, @auto_latency,
    @auto_bitrate, @auto_wait, @follow_video, @tracking_enabled);

-- name: UpdateGroup :execrows
UPDATE groups SET name = @name, kind = @kind, preferred_leader = @preferred_leader, latency_ms = @latency_ms,
    bitrate_kbps = @bitrate_kbps, pcm = @pcm, wait_ms = @wait_ms, key_sealed = @key_sealed, auto_latency = @auto_latency,
    auto_bitrate = @auto_bitrate, auto_wait = @auto_wait, follow_video = @follow_video,
    tracking_enabled = @tracking_enabled, updated_at = now()
WHERE id = @id;

-- name: SetGroupKey :execrows
UPDATE groups SET key_sealed = @key_sealed, updated_at = now() WHERE id = @id;

-- name: DeleteGroup :execrows
DELETE FROM groups WHERE id = $1;

-- name: GetGroup :one
SELECT id, name, kind, preferred_leader, latency_ms, bitrate_kbps, pcm, wait_ms, key_sealed, created_at, updated_at,
    auto_latency, auto_bitrate, auto_wait, follow_video, tracking_enabled
FROM groups WHERE id = $1;

-- name: LockGroup :one
SELECT id, name, kind, preferred_leader, latency_ms, bitrate_kbps, pcm, wait_ms, key_sealed, created_at, updated_at,
    auto_latency, auto_bitrate, auto_wait, follow_video, tracking_enabled
FROM groups WHERE id = $1 FOR UPDATE;

-- name: ListGroups :many
SELECT id, name, kind, preferred_leader, latency_ms, bitrate_kbps, pcm, wait_ms, key_sealed, created_at, updated_at,
    auto_latency, auto_bitrate, auto_wait, follow_video, tracking_enabled
FROM groups ORDER BY created_at, id;

-- name: ListAllGroupMembers :many
SELECT group_id, device_id, channel, trim_ms, position, trim_us FROM group_members
ORDER BY group_id, position, device_id;

-- name: ListGroupMembers :many
SELECT group_id, device_id, channel, trim_ms, position, trim_us FROM group_members WHERE group_id = $1
ORDER BY position, device_id;

-- name: DeleteGroupMembers :exec
-- a whole-group update removes only departures: retained rows keep their output calibrations through the fk
DELETE FROM group_members WHERE group_id = @group_id AND NOT (device_id = ANY(@device_ids::uuid[]));

-- name: AddGroupMember :exec
INSERT INTO group_members (group_id, device_id, channel, trim_ms, trim_us, position)
VALUES (@group_id, @device_id, @channel, @trim_ms, @trim_us, @position)
ON CONFLICT (group_id, device_id)
DO UPDATE SET channel = EXCLUDED.channel, trim_ms = EXCLUDED.trim_ms, trim_us = EXCLUDED.trim_us,
    position = EXCLUDED.position;

-- name: SetGroupMemberTrim :execrows
-- a calibration's trims: the member's own row alone, in its group
UPDATE group_members SET trim_ms = @trim_ms, trim_us = @trim_us WHERE group_id = @group_id AND device_id = @device_id;

-- name: GroupsOfDevices :many
-- the groups the devices are in now, each device once
SELECT group_id, device_id FROM group_members WHERE device_id = ANY(@device_ids::uuid[]) ORDER BY device_id;

-- name: RemoveGroupMembers :exec
DELETE FROM group_members WHERE device_id = ANY(@device_ids::uuid[]);

-- name: GroupOfDevice :one
SELECT group_id FROM group_members WHERE device_id = $1;

-- name: SetGroupPreferredLeader :execrows
UPDATE groups SET preferred_leader = @preferred_leader, updated_at = now() WHERE id = @id;

-- name: ListAllGroupOutputTrims :many
SELECT group_id, device_id, output_id, trim_us, updated_at, ultrasonic FROM group_output_trims
ORDER BY group_id, device_id, output_id;

-- name: ListGroupOutputTrims :many
SELECT group_id, device_id, output_id, trim_us, updated_at, ultrasonic FROM group_output_trims
WHERE group_id = $1 ORDER BY device_id, output_id;

-- name: SetGroupOutputTrim :execrows
INSERT INTO group_output_trims (group_id, device_id, output_id, trim_us, ultrasonic)
SELECT @group_id, @device_id, @output_id, @trim_us, @ultrasonic
WHERE EXISTS (SELECT 1 FROM group_members WHERE group_id = @group_id AND device_id = @device_id)
ON CONFLICT (group_id, device_id, output_id)
DO UPDATE SET trim_us = EXCLUDED.trim_us, ultrasonic = EXCLUDED.ultrasonic, updated_at = now();

-- name: ApplyGroupTrackingCorrection :one
-- a live correction moves one active output's trim atomically, never past the same bounds as a calibration
UPDATE group_output_trims SET trim_us = trim_us + @correction_us, updated_at = now()
WHERE group_id = @group_id AND device_id = @device_id AND output_id = @output_id
    AND trim_us + @correction_us BETWEEN -500000 AND 500000
RETURNING trim_us;

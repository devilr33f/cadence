-- name: StartPlayHistory :one
-- a quick repeat continues its recent row rather than adding another
WITH recent AS (
    SELECT id
    FROM play_history
    WHERE play_history.device_id = @device_id::uuid AND play_history.dedupe_key = @dedupe_key
      AND coalesce(play_history.ended_at, play_history.started_at) >= @recent_after::timestamptz
    ORDER BY play_history.started_at DESC, play_history.id DESC
    LIMIT 1
), resumed AS (
    UPDATE play_history h
    SET ended_at = NULL,
        turn_id = CASE WHEN h.turn_id = '' THEN @turn_id ELSE h.turn_id END,
        provider_song_id = CASE WHEN @provider_song_id::text <> '' THEN @provider_song_id::text ELSE h.provider_song_id END,
        web_url = CASE WHEN @web_url::text <> '' THEN @web_url::text ELSE h.web_url END,
        provider_instance = CASE WHEN @provider_instance::text <> '' THEN @provider_instance::text ELSE h.provider_instance END
    FROM recent r
    WHERE h.id = r.id
    RETURNING h.id, h.started_at, h.played_ms
), inserted AS (
    INSERT INTO play_history (
        device_id, source, provider, kind, title, artist, album, artwork_url, artwork_hash,
        catalog_id, turn_id, dedupe_key, started_at, provider_song_id, web_url, provider_instance
    )
    SELECT @device_id::uuid, @source, @provider, @kind, @title, @artist, @album, @artwork_url, @artwork_hash,
           @catalog_id, @turn_id, @dedupe_key, @started_at::timestamptz, @provider_song_id::text, @web_url::text, @provider_instance::text
    WHERE NOT EXISTS (SELECT 1 FROM recent)
    RETURNING id, started_at, played_ms
)
SELECT id, started_at, played_ms FROM resumed
UNION ALL
SELECT id, started_at, played_ms FROM inserted;

-- name: AnnotatePlayHistory :exec
-- a pending assistant play can name its session just after the first report
UPDATE play_history
SET source = @source, provider = @provider, kind = @kind, title = @title, artist = @artist, album = @album,
    artwork_url = @artwork_url, artwork_hash = @artwork_hash, catalog_id = @catalog_id, turn_id = @turn_id,
    dedupe_key = @dedupe_key, provider_song_id = @provider_song_id::text, web_url = @web_url::text,
    provider_instance = @provider_instance::text
WHERE id = @id;

-- name: CheckpointPlayHistory :exec
UPDATE play_history SET played_ms = @played_ms WHERE id = @id;

-- name: FinishPlayHistory :exec
UPDATE play_history SET ended_at = @ended_at::timestamptz, played_ms = @played_ms WHERE id = @id;

-- name: PlayHistoryPage :many
SELECT h.id, h.device_id, d.name AS device_name, h.source, h.provider, h.kind, h.title, h.artist, h.album,
       h.artwork_url, h.artwork_hash, h.catalog_id, h.turn_id, h.started_at, h.ended_at, h.played_ms,
       h.provider_song_id, h.web_url, coalesce(s.feedback, 'none') AS feedback,
       (s.favorited_at IS NOT NULL)::boolean AS favorite
FROM play_history h
JOIN devices d ON d.id = h.device_id
LEFT JOIN music_song_state s ON s.provider = h.provider AND s.provider_song_id = h.provider_song_id
WHERE (sqlc.narg('device_id')::uuid IS NULL OR h.device_id = sqlc.narg('device_id')::uuid)
  AND (h.started_at, h.id) < (@before_at::timestamptz, @before_id::bigint)
ORDER BY h.started_at DESC, h.id DESC
LIMIT @max_rows::int;

-- name: DeletePlayHistory :execrows
DELETE FROM play_history WHERE id = @id;

-- name: PrunePlayHistory :one
WITH deleted AS (
    DELETE FROM play_history WHERE started_at < @older_than::timestamptz RETURNING 1
)
SELECT count(*)::bigint AS rows FROM deleted;

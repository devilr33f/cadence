-- name: UpsertMusicSongState :one
INSERT INTO music_song_state (
    provider, provider_song_id, instance, video_id, title, artist, album, duration_s,
    artwork_url, web_url, feedback, favorited_at, created_at, updated_at
)
VALUES (
    @provider, @provider_song_id, @instance, @video_id, @title, @artist, @album, @duration_s,
    @artwork_url, @web_url, @feedback,
    CASE WHEN @feedback = 'like' THEN @now::timestamptz END,
    @now::timestamptz, @now::timestamptz
)
ON CONFLICT (provider, provider_song_id) DO UPDATE
SET instance = CASE WHEN EXCLUDED.instance <> '' THEN EXCLUDED.instance ELSE music_song_state.instance END,
    video_id = CASE WHEN EXCLUDED.video_id <> '' THEN EXCLUDED.video_id ELSE music_song_state.video_id END,
    title = EXCLUDED.title,
    artist = EXCLUDED.artist,
    album = EXCLUDED.album,
    duration_s = EXCLUDED.duration_s,
    artwork_url = EXCLUDED.artwork_url,
    web_url = EXCLUDED.web_url,
    feedback = EXCLUDED.feedback,
    favorited_at = CASE
        WHEN EXCLUDED.feedback = 'like' THEN coalesce(music_song_state.favorited_at, EXCLUDED.favorited_at)
        ELSE NULL
    END,
    updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: DeleteMusicSongState :execrows
DELETE FROM music_song_state WHERE provider = @provider AND provider_song_id = @provider_song_id;

-- name: GetMusicSongState :one
SELECT * FROM music_song_state WHERE provider = @provider AND provider_song_id = @provider_song_id;

-- name: LatestMusicSongHistory :one
SELECT provider_instance, title, artist, album, artwork_url, web_url
FROM play_history
WHERE provider = @provider AND provider_song_id = @provider_song_id
ORDER BY started_at DESC, id DESC
LIMIT 1;

-- name: ListMusicFavorites :many
SELECT * FROM music_song_state
WHERE favorited_at IS NOT NULL
  AND (@query::text = '' OR title ILIKE '%' || @query::text || '%' OR artist ILIKE '%' || @query::text || '%' OR album ILIKE '%' || @query::text || '%')
ORDER BY favorited_at DESC, provider, provider_song_id
LIMIT @max_rows::int;

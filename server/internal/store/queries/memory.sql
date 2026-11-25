-- name: AddMemory :one
INSERT INTO memories (scope, device_id, kind, text, embedding, embed_model, pinned, confidence, evidence, source, status,
                      replaces, proposal)
VALUES (@scope, sqlc.narg(device_id), @kind, @text, CAST(CAST(sqlc.narg(embedding) AS text) AS vector), sqlc.narg(embed_model),
        @pinned, @confidence, @evidence::bigint[], @source, @status, sqlc.narg(replaces), sqlc.narg(proposal))
RETURNING id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
          last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal;

-- name: GetMemory :one
SELECT id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
       last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal
FROM memories WHERE id = @id;

-- name: UpdateMemory :one
UPDATE memories SET
    text = coalesce(sqlc.narg(text), text),
    kind = coalesce(sqlc.narg(kind), kind),
    scope = coalesce(sqlc.narg(scope), scope),
    device_id = CASE WHEN sqlc.narg(scope)::text IS NULL THEN device_id ELSE sqlc.narg(device_id)::uuid END,
    pinned = coalesce(sqlc.narg(pinned), pinned),
    status = coalesce(sqlc.narg(status), status),
    source = coalesce(sqlc.narg(source), source),
    confidence = coalesce(sqlc.narg(confidence), confidence),
    evidence = coalesce(sqlc.narg(evidence)::bigint[], evidence),
    embedding = CASE WHEN sqlc.narg(text)::text IS NULL THEN embedding ELSE NULL END,
    embed_model = CASE WHEN sqlc.narg(text)::text IS NULL THEN embed_model ELSE NULL END,
    updated_at = now()
WHERE id = @id
RETURNING id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
          last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal;

-- name: UpdateLearnedMemory :one
UPDATE memories SET
    text = @text, kind = @kind, confidence = @confidence, evidence = @evidence::bigint[],
    embedding = CASE WHEN text = @text THEN embedding END,
    embed_model = CASE WHEN text = @text THEN embed_model END,
    updated_at = now()
WHERE id = @id AND source = 'learned' AND NOT pinned AND status = @status AND updated_at = @seen
RETURNING id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
          last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal;

-- name: DeleteLearnedMemory :execrows
DELETE FROM memories
WHERE id = @id AND source = 'learned' AND NOT pinned AND status = @status AND updated_at = @seen;

-- name: SetMemoryEmbedding :execrows
UPDATE memories SET embedding = CAST(CAST(@embedding AS text) AS vector), embed_model = @embed_model WHERE id = @id;

-- name: DeleteMemory :execrows
DELETE FROM memories WHERE id = @id;

-- name: ListMemories :many
SELECT id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
       last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal
FROM memories
WHERE (sqlc.narg(scope)::text IS NULL OR scope = sqlc.narg(scope))
  AND (sqlc.narg(device_id)::uuid IS NULL OR device_id = sqlc.narg(device_id))
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(pinned)::bool IS NULL OR pinned = sqlc.narg(pinned))
ORDER BY created_at, id;

-- name: ListMemoriesPage :many
SELECT id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
       last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal
FROM memories
WHERE (sqlc.narg(scope)::text IS NULL OR scope = sqlc.narg(scope))
  AND (sqlc.narg(device_id)::uuid IS NULL OR device_id = sqlc.narg(device_id))
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(pinned)::bool IS NULL OR pinned = sqlc.narg(pinned))
ORDER BY created_at DESC, id DESC
LIMIT @max_rows::int OFFSET @skip::int;

-- name: NearestMemories :many
SELECT id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
       last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal,
       (1 - (embedding <=> CAST(CAST(@vec AS text) AS vector)))::float8 AS similarity
FROM memories
WHERE (status = 'active' OR (@with_pending::bool AND status = 'pending' AND replaces IS NULL))
  AND embed_model = @model AND embedding IS NOT NULL
  AND ((@include_global::bool AND scope = 'global') OR (scope = 'device' AND device_id = sqlc.narg(device_id)))
  AND (1 - (embedding <=> CAST(CAST(@vec AS text) AS vector))) >= @min_similarity::float8
ORDER BY embedding <=> CAST(CAST(@vec AS text) AS vector), id
LIMIT @max_rows::int;

-- name: PinnedMemories :many
SELECT id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
       last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal
FROM memories
WHERE status = 'active' AND pinned
  AND ((@include_global::bool AND scope = 'global') OR (scope = 'device' AND device_id = sqlc.narg(device_id)))
ORDER BY created_at, id;

-- name: LockMemoryScope :exec
SELECT pg_advisory_xact_lock(hashtextextended(@scope_key::text, 0));

-- name: HitMemories :execrows
UPDATE memories SET hits = hits + 1, last_hit_at = now() WHERE id = ANY(@ids::uuid[]);

-- name: CountMemoriesByScope :one
SELECT count(*)::int FROM memories
WHERE status = 'active' AND scope = @scope AND device_id IS NOT DISTINCT FROM sqlc.narg(device_id)::uuid;

-- name: MemoryEvictionCandidates :many
SELECT id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
       last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal
FROM memories
WHERE status = 'active' AND NOT pinned AND source = 'learned' AND NOT (id = ANY(@keep::uuid[]))
  AND scope = @scope AND device_id IS NOT DISTINCT FROM sqlc.narg(device_id)::uuid
ORDER BY hits * power(0.5, extract(epoch FROM now() - coalesce(last_hit_at, created_at)) / 86400.0 / 14.0), created_at, id
LIMIT @max_rows::int;

-- name: StaleModelMemories :many
SELECT id, scope, device_id, kind, text, embed_model, (embedding IS NOT NULL)::bool AS has_embedding, pinned, hits,
       last_hit_at, confidence, evidence, source, status, created_at, updated_at, replaces, proposal
FROM memories
WHERE embed_model IS DISTINCT FROM @model::text
ORDER BY created_at, id
LIMIT @max_rows::int;

-- name: AddCompaction :one
INSERT INTO conversation_compactions (device_id, upto_message_id, summary, open_threads, task_refs, model)
VALUES (@device_id, @upto_message_id, @summary, @open_threads::jsonb, @task_refs::jsonb, @model)
RETURNING *;

-- name: LatestCompaction :one
SELECT * FROM conversation_compactions WHERE device_id = @device_id ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListCompactions :many
SELECT * FROM conversation_compactions WHERE device_id = @device_id ORDER BY created_at DESC, id DESC LIMIT @max_rows::int;

-- name: AddLearningRun :one
INSERT INTO memory_learning_runs (compaction_id, device_id, applied, dropped, pending, memory_ids)
VALUES (@compaction_id, @device_id, @applied, @dropped, @pending, @memory_ids::uuid[])
RETURNING *;

-- name: ListLearningRuns :many
SELECT * FROM memory_learning_runs WHERE device_id = @device_id ORDER BY created_at DESC, id DESC LIMIT @max_rows::int;

-- name: SetMemoryEmbeddingIfText :execrows
UPDATE memories SET embedding = CAST(CAST(@embedding AS text) AS vector), embed_model = @embed_model
WHERE id = @id AND text = @text;

-- name: CompactionCandidates :many
SELECT d.id AS device_id, pending.newest_id::bigint AS newest_id, pending.newest_at::timestamptz AS newest_at
FROM devices d
CROSS JOIN LATERAL (
    SELECT max(m.id) AS newest_id, max(m.created_at) AS newest_at
    FROM conversation_messages m
    WHERE m.device_id = d.id AND m.id > coalesce((
        SELECT c.upto_message_id FROM conversation_compactions c
        WHERE c.device_id = d.id ORDER BY c.created_at DESC, c.id DESC LIMIT 1
    ), 0)
) pending
WHERE pending.newest_id IS NOT NULL
ORDER BY d.id;

-- name: UncompactedChars :one
SELECT coalesce(sum(length(m.content::text)), 0)::bigint
FROM conversation_messages m
WHERE m.device_id = @device_id::uuid AND m.id > coalesce((
    SELECT c.upto_message_id FROM conversation_compactions c
    WHERE c.device_id = @device_id::uuid ORDER BY c.created_at DESC, c.id DESC LIMIT 1
), 0);

-- name: PruneCompactions :execrows
DELETE FROM conversation_compactions old
WHERE old.device_id = @device_id::uuid AND old.id NOT IN (
    SELECT c.id FROM conversation_compactions c
    WHERE c.device_id = @device_id::uuid ORDER BY c.created_at DESC, c.id DESC LIMIT @keep::int
);

-- name: PrunePendingMemories :execrows
DELETE FROM memories WHERE id IN (
    SELECT p.id FROM memories p
    WHERE p.status = 'pending' AND p.source = 'learned' AND NOT p.pinned AND p.scope = @scope
      AND p.device_id IS NOT DISTINCT FROM sqlc.narg(device_id)::uuid
    ORDER BY p.created_at DESC, p.id DESC OFFSET @keep::int
);

-- name: DeleteMemoryProposals :execrows
DELETE FROM memories WHERE replaces = @replaces AND status = 'pending';

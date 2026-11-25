-- name: AppendConversationTurn :exec
-- a turn's row and its messages in one statement, so both land or neither does
WITH turn AS (
    INSERT INTO conversation_turns (device_id, turn_id, trigger, mode, prompt_tokens, completion_tokens, skills, failure)
    VALUES (@device_id::uuid, @turn_id::text, sqlc.narg('trigger')::text, sqlc.narg('mode')::text,
            sqlc.narg('prompt_tokens')::int, sqlc.narg('completion_tokens')::int, sqlc.narg('skills')::text[],
            sqlc.narg('failure')::jsonb)
)
INSERT INTO conversation_messages (device_id, turn_id, seq, role, content)
SELECT @device_id::uuid, @turn_id::text, unnest(@seqs::int[]), unnest(@roles::text[]), unnest(@contents::jsonb[]);

-- name: RecordConversationFailure :exec
-- a voice-stage failure can arrive after the agent committed its reply (tts),
-- or before an agent turn existed (stt). keep any messages and facts it stored.
INSERT INTO conversation_turns (device_id, turn_id, trigger, mode, failure)
VALUES (@device_id::uuid, @turn_id::text, sqlc.narg('trigger')::text, sqlc.narg('mode')::text, @failure::jsonb)
ON CONFLICT (device_id, turn_id) DO UPDATE SET
    failure = EXCLUDED.failure,
    trigger = coalesce(conversation_turns.trigger, EXCLUDED.trigger),
    mode = coalesce(conversation_turns.mode, EXCLUDED.mode);

-- name: ConversationStart :one
-- 0 until the device's conversation was reset; also proves the device exists
SELECT conversation_after_id FROM devices WHERE id = @device_id::uuid;

-- name: ResetConversation :one
-- the boundary never moves back, and a device with no messages starts at 0
UPDATE devices d
SET conversation_after_id = greatest(d.conversation_after_id, coalesce((
    SELECT max(m.id) FROM conversation_messages m WHERE m.device_id = d.id
), 0))
WHERE d.id = @device_id::uuid
RETURNING conversation_after_id;

-- name: RecentConversationTurns :many
-- notice and reset turns are outside text the model reads back: an unread
-- block carries a notice, and a reset only marks where the next context starts
SELECT m.turn_id
FROM conversation_messages m
WHERE m.device_id = @device_id AND m.id > @after_id
  AND NOT EXISTS (SELECT 1 FROM conversation_turns t
                  WHERE t.device_id = m.device_id AND t.turn_id = m.turn_id AND t.trigger IN ('notice', 'reset'))
GROUP BY m.turn_id
ORDER BY min(m.id) DESC
LIMIT @max_turns::int;

-- name: ConversationTurnMessages :many
SELECT turn_id, content
FROM conversation_messages
WHERE device_id = @device_id AND id > @after_id AND turn_id = ANY(@turn_ids::text[])
ORDER BY id;

-- name: ConversationMessagesAfter :many
-- with the trigger of each message's turn, NULL for a turn that recorded none
SELECT newest.id, newest.content, t.trigger FROM (
    SELECT m.id, m.content, m.device_id, m.turn_id
    FROM conversation_messages m
    WHERE m.device_id = @device_id AND m.id > @after_id AND m.created_at < @before
      AND NOT EXISTS (SELECT 1 FROM conversation_turns n
                      WHERE n.device_id = m.device_id AND n.turn_id = m.turn_id AND n.trigger IN ('notice', 'reset'))
    ORDER BY m.id DESC
    LIMIT @max_rows::int
) newest
LEFT JOIN conversation_turns t ON t.device_id = newest.device_id AND t.turn_id = newest.turn_id
ORDER BY newest.id;

-- name: ConversationMessagesThrough :many
-- a reset's closed segment: committed messages through upto_id, however fresh,
-- with reset turns themselves left out; past the limit the newest ones
SELECT newest.id, newest.content, t.trigger FROM (
    SELECT m.id, m.content, m.device_id, m.turn_id
    FROM conversation_messages m
    WHERE m.device_id = @device_id AND m.id > @after_id AND m.id <= @upto_id
      AND NOT EXISTS (SELECT 1 FROM conversation_turns n
                      WHERE n.device_id = m.device_id AND n.turn_id = m.turn_id AND n.trigger IN ('notice', 'reset'))
    ORDER BY m.id DESC
    LIMIT @max_rows::int
) newest
LEFT JOIN conversation_turns t ON t.device_id = newest.device_id AND t.turn_id = newest.turn_id
ORDER BY newest.id;

-- name: ConversationTurnPage :many
-- a device's turns newest first, from before the cursor's (before_at,
-- before_id): the first page's cursor is infinity
SELECT turn_id, trigger, mode, prompt_tokens, completion_tokens, created_at, failure
FROM conversation_turns
WHERE device_id = @device_id AND (created_at, turn_id) < (@before_at::timestamptz, @before_id::text)
ORDER BY created_at DESC, turn_id DESC
LIMIT @max_rows::int;

-- name: GetConversationTurn :one
SELECT turn_id, trigger, mode, prompt_tokens, completion_tokens, created_at, skills, failure
FROM conversation_turns
WHERE device_id = @device_id AND turn_id = @turn_id;

-- name: ConversationTurnsMessages :many
-- each message whole, and with a tool message the code its result carries,
-- read exactly as ConversationTurnsSummaries reads it
SELECT m.turn_id, m.seq, m.content,
       left(coalesce(
           CASE WHEN jsonb_typeof(result.doc->'code') = 'string' THEN nullif(result.doc->>'code', '') END,
           CASE WHEN jsonb_typeof(result.doc->'error') = 'string' THEN nullif(result.doc->>'error', '') END,
           CASE WHEN jsonb_typeof(result.doc->'error'->'code') = 'string' THEN nullif(result.doc->'error'->>'code', '') END,
           ''), 64)::text AS code
FROM conversation_messages m
CROSS JOIN LATERAL (
    SELECT CASE WHEN m.role = 'tool' AND octet_length(m.content->>'content') <= 4096
                     AND pg_input_is_valid(m.content->>'content', 'jsonb')
                THEN (m.content->>'content')::jsonb END AS doc
) result
WHERE m.device_id = @device_id AND m.turn_id = ANY(@turn_ids::text[])
ORDER BY m.turn_id, m.seq;

-- name: PruneConversations :one
-- turns and messages created before older_than, counted
WITH turns AS (
    DELETE FROM conversation_turns t WHERE t.created_at < @older_than::timestamptz RETURNING 1
), messages AS (
    DELETE FROM conversation_messages m WHERE m.created_at < @older_than::timestamptz RETURNING 1
)
SELECT (SELECT count(*) FROM messages)::bigint AS messages, (SELECT count(*) FROM turns)::bigint AS turns;

-- name: ConversationTurnsSummaries :many
-- what a page's list shows of each message, never a message whole: the role,
-- a user or assistant text cut to 2000 characters, an assistant's tool calls
-- by id and name, and the call a tool message answers with the code its
-- result carries (a string code or error field, else its error object's
-- code), read from results of at most 4 KiB, the runner's error envelopes
SELECT m.turn_id, m.seq, m.role,
       coalesce(CASE WHEN m.role IN ('user', 'assistant') THEN left(m.content->>'content', 2000) END, '')::text AS text,
       ARRAY(SELECT coalesce(c.value->>'id', '') FROM jsonb_array_elements(calls.list) WITH ORDINALITY AS c(value, i) ORDER BY c.i)::text[] AS call_ids,
       ARRAY(SELECT coalesce(c.value->>'name', '') FROM jsonb_array_elements(calls.list) WITH ORDINALITY AS c(value, i) ORDER BY c.i)::text[] AS call_names,
       coalesce(CASE WHEN m.role = 'tool' THEN m.content->>'toolCallId' END, '')::text AS tool_call_id,
       left(coalesce(
           CASE WHEN jsonb_typeof(result.doc->'code') = 'string' THEN nullif(result.doc->>'code', '') END,
           CASE WHEN jsonb_typeof(result.doc->'error') = 'string' THEN nullif(result.doc->>'error', '') END,
           CASE WHEN jsonb_typeof(result.doc->'error'->'code') = 'string' THEN nullif(result.doc->'error'->>'code', '') END,
           ''), 64)::text AS code
FROM conversation_messages m
CROSS JOIN LATERAL (
    SELECT CASE WHEN m.role = 'assistant' AND jsonb_typeof(m.content->'toolCalls') = 'array'
                THEN m.content->'toolCalls' ELSE '[]'::jsonb END AS list
) calls
CROSS JOIN LATERAL (
    SELECT CASE WHEN m.role = 'tool' AND octet_length(m.content->>'content') <= 4096
                     AND pg_input_is_valid(m.content->>'content', 'jsonb')
                THEN (m.content->>'content')::jsonb END AS doc
) result
WHERE m.device_id = @device_id AND m.turn_id = ANY(@turn_ids::text[])
ORDER BY m.turn_id, m.seq;

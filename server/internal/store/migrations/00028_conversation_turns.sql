-- +goose Up
-- a turn's own facts beside its messages (ruling M7bs-R4): the trigger that
-- started it, the mode it ended in and its tokens, written in the statement
-- that writes its messages and pruned with them. A turn stored before has its
-- messages' start and none of them.
CREATE TABLE conversation_turns (
    device_id         uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    turn_id           text        NOT NULL,
    trigger           text,
    mode              text,
    prompt_tokens     int,
    completion_tokens int,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, turn_id)
);

-- a device's history is read newest first, a page at a time
CREATE INDEX conversation_turns_device_created ON conversation_turns (device_id, created_at DESC, turn_id DESC);

-- and the messages of a page by their turns
CREATE INDEX conversation_messages_device_turn ON conversation_messages (device_id, turn_id, seq);

INSERT INTO conversation_turns (device_id, turn_id, created_at)
SELECT device_id, turn_id, min(created_at) FROM conversation_messages GROUP BY device_id, turn_id;

-- +goose Down
DROP INDEX conversation_messages_device_turn;
DROP TABLE conversation_turns;

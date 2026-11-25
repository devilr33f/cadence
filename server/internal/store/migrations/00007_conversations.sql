-- +goose Up
CREATE TABLE conversation_messages (
    id         bigserial PRIMARY KEY,
    device_id  uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    turn_id    text        NOT NULL,
    seq        int         NOT NULL,
    role       text        NOT NULL,
    content    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX conversation_messages_device_created ON conversation_messages (device_id, created_at);

-- +goose Down
DROP TABLE conversation_messages;

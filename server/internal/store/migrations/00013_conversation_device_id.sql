-- +goose Up
-- history, compaction segments and their candidates read a device's messages after a message id
CREATE INDEX conversation_messages_device_id ON conversation_messages (device_id, id);

-- +goose Down
DROP INDEX conversation_messages_device_id;

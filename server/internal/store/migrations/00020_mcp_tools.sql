-- +goose Up
-- the owner's say over each tool an mcp server listed (M5d-D4): Write and
-- Untrusted, off by default and woken-only until marked otherwise. A tool
-- whose hash (the endpoint, its description, schema and annotations) moved
-- past the acknowledged one is changed and withdrawn until the owner
-- acknowledges it (Ruling M5d-R2). local_name is the name the model and the
-- devices' tools settings know: unique across every instance and kept while
-- its row lives, so it never passes to another tool, of this server or any
-- other. A row stays while a provider instance of its name exists, however
-- long its tool is out of the listing, so a tool that comes back is compared
-- with its last acknowledged hash (Ruling M5d-R11); gone_since is when a
-- prune first found the instance gone, and its rows go 30 days later.
CREATE TABLE mcp_tools (
    instance        text        NOT NULL CHECK (char_length(instance) BETWEEN 1 AND 64),
    remote_name     text        NOT NULL CHECK (char_length(remote_name) BETWEEN 1 AND 128),
    local_name      text        NOT NULL UNIQUE CHECK (local_name ~ '^mcp_[a-z][a-z0-9_]{0,15}_[A-Za-z0-9_-]*$' AND char_length(local_name) <= 64),
    schema_hash     text        NOT NULL CHECK (schema_hash ~ '^[0-9a-f]{64}$'),
    acked_hash      text        NOT NULL CHECK (acked_hash ~ '^[0-9a-f]{64}$'),
    changed         boolean     NOT NULL GENERATED ALWAYS AS (schema_hash <> acked_hash) STORED,
    effects         text        NOT NULL DEFAULT 'write' CHECK (effects IN ('read', 'write')),
    trust           text        NOT NULL DEFAULT 'untrusted' CHECK (trust IN ('trusted', 'untrusted')),
    enabled_default boolean     NOT NULL DEFAULT false,
    any_turn        boolean     NOT NULL DEFAULT false,
    first_seen      timestamptz NOT NULL DEFAULT now(),
    last_seen       timestamptz NOT NULL DEFAULT now(),
    gone_since      timestamptz,
    PRIMARY KEY (instance, remote_name)
);

-- +goose Down
DROP TABLE mcp_tools;

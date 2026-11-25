-- +goose Up
-- what a device follows in outside systems (the subscriptions hub): a topic
-- <module>:<instance>:<path...>, the events it asks for and how they reach
-- the device. module and instance are the topic's first two segments, kept
-- apart so a provider delete finds the subscriptions naming its instance
-- (409 in_use). one subscription per device and topic: a new one replaces it.
CREATE TABLE subscriptions (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id  uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    topic      text        NOT NULL CHECK (char_length(topic) BETWEEN 1 AND 512),
    module     text        NOT NULL,
    instance   text        NOT NULL,
    events     text[]      NOT NULL,
    delivery   text        NOT NULL CHECK (delivery IN ('speaker', 'agent', 'mute')),
    until      text        NOT NULL CHECK (until IN ('unsubscribed', 'next_stop')),
    created_by text        NOT NULL CHECK (created_by IN ('voice', 'api')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (device_id, topic)
);

CREATE INDEX subscriptions_instance ON subscriptions (instance);

-- one update as it reached one device: its text is the outside system's and
-- user content (the DB and the admin API alone, never a log), kept 14 days as
-- a transcript (D8). state: pending waits for the device (online, idle,
-- outside quiet hours, not busy), delivered was spoken, inbox waits for the
-- next woken turn alone (afterBusy: inbox). held_busy marks one a busy device
-- held. seen_turn is the woken turn that read it in its unread block.
CREATE TABLE subscription_items (
    id              bigserial   PRIMARY KEY,
    device_id       uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    subscription_id uuid        REFERENCES subscriptions ON DELETE SET NULL,
    module          text        NOT NULL,
    instance        text        NOT NULL,
    topic           text        NOT NULL,
    event           text        NOT NULL,
    kind            text        NOT NULL DEFAULT '',
    title           text        NOT NULL DEFAULT '',
    text            text        NOT NULL DEFAULT '',
    ref_id          text        NOT NULL DEFAULT '',
    question        jsonb,
    token           text        NOT NULL DEFAULT '' CHECK (token ~ '^[A-Za-z0-9_-]{0,64}$'),
    delivery        text        NOT NULL CHECK (delivery IN ('speaker', 'agent')),
    state           text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivered', 'inbox')),
    held_busy       boolean     NOT NULL DEFAULT false,
    at              timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    delivered_at    timestamptz,
    seen_at         timestamptz,
    seen_turn       text        NOT NULL DEFAULT '' CHECK (char_length(seen_turn) <= 64),
    answered_at     timestamptz,
    withdrawn_at    timestamptz
);

CREATE INDEX subscription_items_pending ON subscription_items (device_id, id) WHERE state = 'pending';
CREATE INDEX subscription_items_unread ON subscription_items (device_id, id) WHERE seen_at IS NULL OR (question IS NOT NULL AND answered_at IS NULL AND withdrawn_at IS NULL);
CREATE INDEX subscription_items_ref ON subscription_items (module, instance, ref_id);
CREATE INDEX subscription_items_created ON subscription_items (created_at);

-- which question, by its token, a device's user was told: spoken there
-- (turn_id '') or read by a woken turn there; a voice approval answers only a
-- question told before its own turn. the first telling stays
CREATE TABLE subscription_told (
    module    text        NOT NULL,
    instance  text        NOT NULL,
    ref_id    text        NOT NULL,
    token     text        NOT NULL CHECK (token ~ '^[A-Za-z0-9_-]{1,64}$'),
    device_id uuid        NOT NULL REFERENCES devices ON DELETE CASCADE,
    turn_id   text        NOT NULL DEFAULT '' CHECK (char_length(turn_id) <= 64),
    at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (module, instance, ref_id, token, device_id)
);

-- +goose Down
DROP TABLE subscription_told;
DROP TABLE subscription_items;
DROP TABLE subscriptions;

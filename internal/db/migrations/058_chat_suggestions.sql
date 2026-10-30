-- Suggestions from the chat housekeeping pass (merge two chats, move messages to a chat of their own, archive a stale chat, re-title a
-- chat that drifted). The key identifies what is suggested, so a dismissed suggestion is not made again.
CREATE TABLE IF NOT EXISTS chat_suggestions (
    id         bigserial PRIMARY KEY,
    kind       text        NOT NULL,
    key        text        NOT NULL,
    topics     text[]      NOT NULL DEFAULT '{}',
    payload    jsonb       NOT NULL DEFAULT '{}',
    text       text        NOT NULL,
    status     text        NOT NULL DEFAULT 'open',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (kind, key)
);

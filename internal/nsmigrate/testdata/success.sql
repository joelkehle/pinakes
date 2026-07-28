CREATE TABLE agents (
    agent_id TEXT PRIMARY KEY,
    secret TEXT NOT NULL,
    callback_url TEXT NOT NULL,
    description TEXT NOT NULL,
    meta TEXT
);

CREATE TABLE conversations (
    conversation_id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    participants TEXT NOT NULL,
    meta TEXT
);

CREATE TABLE messages (
    message_id TEXT PRIMARY KEY,
    from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL,
    body TEXT NOT NULL,
    meta TEXT,
    attachments TEXT NOT NULL
);

CREATE TABLE deliveries (
    target_agent_id TEXT NOT NULL,
    delivery_seq INTEGER NOT NULL,
    message_id TEXT NOT NULL,
    last_error TEXT NOT NULL,
    PRIMARY KEY (target_agent_id, delivery_seq)
);

CREATE TABLE delivery_cursors (
    target_agent_id TEXT PRIMARY KEY,
    next_seq INTEGER NOT NULL,
    acknowledged_cursor INTEGER NOT NULL
);

CREATE TABLE idempotency (
    from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL,
    request_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    accepted_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (from_agent, to_agent, request_id)
);

INSERT INTO agents VALUES
    ('atlas-ingest', 'secret-atlas', 'https://invalid/atlas-ingest', 'atlas-ingest must remain in free text', '{"identity":"atlas-ingest"}'),
    ('archive-daemon', 'secret-archive', 'https://invalid/archive-daemon', 'archive-daemon must remain in free text', '{"identity":"archive-daemon"}'),
    ('dual-canary', 'secret-dual', 'https://invalid/dual-canary', 'dual-canary must remain in free text', '{"identity":"dual-canary"}'),
    ('personal.steady-worker', 'secret-steady', 'https://invalid/personal.steady-worker', 'already namespaced', '{"identity":"personal.steady-worker"}');

INSERT INTO conversations VALUES (
    'conv-1',
    'atlas-ingest archive-daemon dual-canary title must remain',
    '["atlas-ingest","archive-daemon","dual-canary","personal.steady-worker"]',
    '{"identity":"atlas-ingest"}'
);

INSERT INTO messages VALUES
    ('msg-1', 'atlas-ingest', 'archive-daemon', 'atlas-ingest and archive-daemon body must remain', '{"identity":"atlas-ingest"}', '[{"name":"archive-daemon"}]'),
    ('msg-2', 'dual-canary', '', 'dual-canary broadcast body must remain', '{"identity":"dual-canary"}', '[]');

INSERT INTO deliveries VALUES
    ('archive-daemon', 0, 'msg-1', 'archive-daemon last_error must remain');

INSERT INTO delivery_cursors VALUES
    ('archive-daemon', 1, 0);

INSERT INTO idempotency VALUES
    ('atlas-ingest', 'archive-daemon', 'request-1', 'msg-1', '2026-07-27T00:00:00Z', '2026-07-28T00:00:00Z'),
    ('dual-canary', '', 'request-2', 'msg-2', '2026-07-27T00:00:00Z', '2026-07-28T00:00:00Z');

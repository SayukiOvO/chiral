-- Notification targets and what each node's alerting state was last known to
-- be, so a restart of the panel does not re-announce everything.
CREATE TABLE alert_targets (
    id      TEXT PRIMARY KEY,
    kind    TEXT NOT NULL CHECK (kind IN ('telegram', 'webhook')),
    name    TEXT NOT NULL,
    -- telegram: "<bot-token>:<chat-id>"; webhook: the URL.
    -- Encrypted at rest: a bot token is a credential like any other.
    config     TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    -- Last delivery outcome, so an operator can see a misconfigured target
    -- without digging through logs.
    last_error   TEXT NOT NULL DEFAULT '',
    last_sent_at INTEGER NOT NULL DEFAULT 0
);

-- One row per node, tracking what has been announced. Kept separate from
-- `nodes` because it is alerting bookkeeping, not node identity.
CREATE TABLE node_alert_state (
    node_id TEXT PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    -- The last state we told anyone about: 1 online, 0 offline.
    announced_online INTEGER NOT NULL,
    -- When the node's observed state last changed, so a flap can be ridden
    -- out before it is worth telling anyone about.
    changed_at    INTEGER NOT NULL,
    announced_at  INTEGER NOT NULL DEFAULT 0,
    -- The state currently being observed, which may not be announced yet.
    observed_online INTEGER NOT NULL
);

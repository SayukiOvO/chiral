CREATE TABLE nodes (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    -- Hash of the one-time join token; cleared once the node registers.
    join_token_hash TEXT,
    -- Hash of the long-term per-node credential; set at registration.
    credential_hash TEXT,
    hostname        TEXT NOT NULL DEFAULT '',
    public_ip       TEXT NOT NULL DEFAULT '',
    agent_version   TEXT NOT NULL DEFAULT '',
    xray_version    TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    registered_at   INTEGER,
    last_seen_at    INTEGER
);

CREATE UNIQUE INDEX idx_nodes_join_token_hash ON nodes (join_token_hash) WHERE join_token_hash IS NOT NULL;
CREATE UNIQUE INDEX idx_nodes_credential_hash ON nodes (credential_hash) WHERE credential_hash IS NOT NULL;

CREATE TABLE node_configs (
    node_id    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    version    INTEGER NOT NULL,
    config     TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    -- 0 = pending, 1 = applied, -1 = failed (error holds the reason).
    applied    INTEGER NOT NULL DEFAULT 0,
    error      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (node_id, version)
);

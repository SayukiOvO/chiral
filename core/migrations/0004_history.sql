-- Time series for the panel's charts.
--
-- Two different shapes, because the two questions are different. Node
-- resources are a gauge — "what was CPU doing at 14:32" — so they are sampled
-- at a fixed cadence and kept for a short window. Traffic is a counter —
-- "how much did this user move today" — so the agent's deltas are accumulated
-- into buckets, which stays bounded no matter how often the agent reports.

-- One resource sample per node per sample interval. Heartbeats arrive far more
-- often than this; the writer drops the ones that land inside an interval
-- already covered.
CREATE TABLE node_samples (
    node_id          TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    at               INTEGER NOT NULL,
    cpu_percent      REAL    NOT NULL DEFAULT 0,
    mem_used_bytes   INTEGER NOT NULL DEFAULT 0,
    mem_total_bytes  INTEGER NOT NULL DEFAULT 0,
    disk_used_bytes  INTEGER NOT NULL DEFAULT 0,
    disk_total_bytes INTEGER NOT NULL DEFAULT 0,
    net_tx_bps       INTEGER NOT NULL DEFAULT 0,
    net_rx_bps       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, at)
);

CREATE INDEX idx_node_samples_at ON node_samples (at);

-- Traffic accumulated into fixed buckets, keyed by the credential's owner and
-- node so the UI can chart a user, a node, or the fleet from one table.
--
-- user_id carries no foreign key on purpose. Deleting a user must not erase
-- the fleet's own history, and traffic legitimately arrives for a credential
-- whose user was removed moments earlier; those land under '' rather than
-- being dropped. SQLite also permits several NULLs in a PRIMARY KEY, which
-- would break the upsert this table depends on — hence a sentinel, not NULL.
CREATE TABLE traffic_buckets (
    bucket_start INTEGER NOT NULL,
    node_id      TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL DEFAULT '',
    up_bytes     INTEGER NOT NULL DEFAULT 0,
    down_bytes   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket_start, node_id, user_id)
);

CREATE INDEX idx_traffic_buckets_start ON traffic_buckets (bucket_start);
CREATE INDEX idx_traffic_buckets_user ON traffic_buckets (user_id, bucket_start);

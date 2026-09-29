-- One durable high-water mark per node/reporter, not one row per poll.
-- Reporter identity survives Agent restarts; only a deliberately new journal
-- introduces a new identity. Old receipts must not be pruned while replay is possible.
CREATE TABLE stats_receipts (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    reporter_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    digest TEXT NOT NULL,
    continuity_indeterminate INTEGER NOT NULL DEFAULT 0 CHECK (continuity_indeterminate IN (0, 1)),
    received_at INTEGER NOT NULL,
    PRIMARY KEY (node_id, reporter_id)
);

-- Keep continuity gaps after the receipt advances. No provider-supplied free
-- text is stored: Core records a fixed reason for the explicit protocol marker.
CREATE TABLE stats_continuity_gaps (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    reporter_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    received_at INTEGER NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY (node_id, reporter_id, sequence)
);

-- These are raw per-runtime dimensions, not an additional charge to a user.
CREATE TABLE runtime_traffic_totals (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    scope TEXT NOT NULL CHECK (scope IN ('inbound', 'outbound')),
    name TEXT NOT NULL,
    up_bytes INTEGER NOT NULL DEFAULT 0 CHECK (up_bytes >= 0),
    down_bytes INTEGER NOT NULL DEFAULT 0 CHECK (down_bytes >= 0),
    PRIMARY KEY (node_id, scope, name)
);

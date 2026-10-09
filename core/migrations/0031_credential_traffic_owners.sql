-- Preserve only the accounting identity of a credential after access is
-- revoked. This table contains no secret and is never an authorization source:
-- config assembly, subscriptions and CredentialOwner continue to read the live
-- credentials table only. RecordStatsBatch may use a retired row solely to
-- attribute a delayed, durably queued acknowledged delta.
CREATE TABLE credential_traffic_owners (
    email         TEXT PRIMARY KEY,
    credential_id TEXT NOT NULL UNIQUE,
    -- A hard user deletion intentionally clears this reference. Delayed bytes
    -- then remain node/fleet traffic and can never recreate or charge a user.
    user_id       TEXT REFERENCES users (id) ON DELETE SET NULL,
    -- Deliberately no foreign key: this is the immutable reporting identity.
    -- Deleting a node revokes its Agent credential and closes its stream, but
    -- must not silently make a historical stats key reusable.
    node_id       TEXT NOT NULL,
    -- Frozen when the credential retires. Keep the legacy REAL contract and
    -- validate it when billing; existing setters historically allow values
    -- that RecordStatsBatch rejects at use time.
    traffic_rate  REAL NOT NULL,
    up_bytes      INTEGER NOT NULL DEFAULT 0 CHECK (up_bytes >= 0),
    down_bytes    INTEGER NOT NULL DEFAULT 0 CHECK (down_bytes >= 0),
    -- NULL means the credential is still live. Non-NULL rows are accounting
    -- tombstones and can never authorize access.
    retired_at    INTEGER
);

-- Backfill all existing credentials without changing their ids, emails,
-- counters or encrypted secrets.
INSERT INTO credential_traffic_owners
    (email, credential_id, user_id, node_id, traffic_rate, up_bytes, down_bytes)
SELECT c.email, c.id, c.user_id, c.node_id,
       COALESCE(x.traffic_rate, r.traffic_rate, n.traffic_rate, 1.0),
       c.up_bytes, c.down_bytes
FROM credentials c
JOIN nodes n ON n.id = c.node_id
LEFT JOIN external_proxies x ON x.id = c.exit_proxy_id
LEFT JOIN node_relays r ON r.id = c.exit_relay_id;

-- Every newly minted live credential receives an accounting owner. A retired
-- email remains present forever, so an accidental reuse fails closed; the Go
-- minting path chooses a fresh stats key only for that retired-key case.
CREATE TRIGGER credential_traffic_owner_insert
AFTER INSERT ON credentials
BEGIN
    INSERT INTO credential_traffic_owners
        (email, credential_id, user_id, node_id, traffic_rate, up_bytes, down_bytes)
    SELECT NEW.email, NEW.id, NEW.user_id, NEW.node_id,
           COALESCE(x.traffic_rate, r.traffic_rate, n.traffic_rate, 1.0),
           NEW.up_bytes, NEW.down_bytes
    FROM nodes n
    LEFT JOIN external_proxies x ON x.id = NEW.exit_proxy_id
    LEFT JOIN node_relays r ON r.id = NEW.exit_relay_id
    WHERE n.id = NEW.node_id;
END;

-- Cascades from user/profile/node/exit deletion all pass through this trigger.
-- The effective rate is already cached, so it remains available even when a
-- parent row is being deleted and can no longer be joined safely.
CREATE TRIGGER credential_traffic_owner_retire
BEFORE DELETE ON credentials
BEGIN
    UPDATE credential_traffic_owners
    SET up_bytes = OLD.up_bytes,
        down_bytes = OLD.down_bytes,
        retired_at = unixepoch()
    WHERE credential_id = OLD.id AND retired_at IS NULL;
END;

-- Keep only active rows aligned with the mutable billing rate. Once retired,
-- the rate is part of the immutable accounting contract for delayed batches.
CREATE TRIGGER credential_traffic_owner_node_rate
AFTER UPDATE OF traffic_rate ON nodes
BEGIN
    UPDATE credential_traffic_owners
    SET traffic_rate = NEW.traffic_rate
    WHERE retired_at IS NULL AND credential_id IN (
        SELECT id FROM credentials
        WHERE node_id = NEW.id AND exit_proxy_id IS NULL AND exit_relay_id IS NULL
    );
END;

CREATE TRIGGER credential_traffic_owner_proxy_rate
AFTER UPDATE OF traffic_rate ON external_proxies
BEGIN
    UPDATE credential_traffic_owners
    SET traffic_rate = NEW.traffic_rate
    WHERE retired_at IS NULL AND credential_id IN (
        SELECT id FROM credentials WHERE exit_proxy_id = NEW.id
    );
END;

CREATE TRIGGER credential_traffic_owner_relay_rate
AFTER UPDATE OF traffic_rate ON node_relays
BEGIN
    UPDATE credential_traffic_owners
    SET traffic_rate = NEW.traffic_rate
    WHERE retired_at IS NULL AND credential_id IN (
        SELECT id FROM credentials
        WHERE exit_relay_id = NEW.id AND exit_proxy_id IS NULL
    );
END;

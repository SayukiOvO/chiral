-- Online source addresses: which addresses each user connects from.
--
-- The whole feature is optional and off by default (CHIRAL_ONLINE_RECORD). With
-- it off, agents never poll and this table stays empty.
--
-- Scope note: Chiral records, it does not enforce. Measured against Xray
-- 26.3.27, neither `xray api rmu` nor `sib` terminates an established session —
-- both only refuse new connections — and the only thing that actually cuts a
-- live session is restarting Xray, which cuts everyone else's on that node too.
-- So there is no "suspend on exceeded" column here, because there is no
-- mechanism for one to drive.

CREATE TABLE user_devices (
    user_id      TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of the address. The primary key cannot be the ciphertext:
    -- AES-GCM is randomised, so the same address seals to different bytes
    -- every time and an upsert would insert a new row per observation.
    -- Hashing gives a stable key; the address itself lives in ip_enc.
    ip_hash      TEXT    NOT NULL,
    -- The address, sealed the same way as credentials.secret and
    -- alert_targets.config (AES-GCM, AAD binds it to this row). A table of
    -- every user's home addresses is exactly the threat model
    -- CHIRAL_SECRET_KEY exists for; leaving it in the clear while every other
    -- secret is sealed would be the one inconsistency worth exploiting.
    ip_enc       TEXT    NOT NULL,
    -- Last node that saw this address. No foreign key, matching
    -- traffic_buckets: deleting a node must not erase the observation, and ''
    -- is the orphan sentinel.
    last_node_id TEXT    NOT NULL DEFAULT '',
    first_seen   INTEGER NOT NULL,
    last_seen    INTEGER NOT NULL,
    PRIMARY KEY (user_id, ip_hash)
);

-- ON DELETE CASCADE, deliberately unlike traffic_buckets, which keeps rows
-- under a '' sentinel when a user is deleted. That is right for byte totals —
-- they still mean something to the fleet after their owner leaves — but an
-- address is not a fleet total, it is where a specific person lives. Leaving it
-- behind under a sentinel, reachable by no UI and therefore reviewed by nobody,
-- is the worst of both.

CREATE INDEX idx_user_devices_last_seen ON user_devices (last_seen);

-- Maximum concurrent source addresses expected for a user. 0 = unlimited,
-- matching the sentinel convention of quota_bytes / expires_at / renew_period.
--
-- A display threshold, not an enforcement one: nothing acts on it. It exists so
-- an operator can see at a glance which accounts are being shared more widely
-- than intended. Note that Xray counts distinct ADDRESSES, not devices — one
-- household behind NAT is one, and one phone moving between wifi and cellular
-- is two — so anything below 3 will produce false alarms.
ALTER TABLE users ADD COLUMN device_limit INTEGER NOT NULL DEFAULT 0;

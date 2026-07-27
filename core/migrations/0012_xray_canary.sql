-- Fleet-wide kernel upgrades: one canary first, then a human decides.
--
-- The state machine, and why each state exists separately:
--
--   canary            the canary node is fetching and switching
--   awaiting_promote  the canary came up; nobody has said to continue
--   promoting         the rest of the fleet is being told to install
--   done              every target reached a terminal phase successfully
--   blocked           something rolled back; the fleet stays where it is
--
-- awaiting_promote is not an implementation detail. An upgrade that continues
-- on its own turns one bad release into a fleet-wide outage at machine speed,
-- and the whole point of a canary is that a person looks at the result.
--
-- blocked is distinct from done-with-failures on purpose. It is the state an
-- admin fixes something and then retries from, which is the last of the five
-- requirements: after the fix, one button takes the fleet to the latest version
-- again. A terminal "done" with a failure count buried in it would have no
-- such button and nothing to hang it off.
CREATE TABLE xray_upgrades (
    id      TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    state   TEXT NOT NULL,
    -- The node that went first. Kept even after promotion so the record says
    -- who took the risk.
    canary_node_id TEXT REFERENCES nodes(id) ON DELETE SET NULL,
    -- Why it is blocked, or how it finished. On a rollback this carries the
    -- kernel's own stderr, which is what the admin has to act on.
    message    TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    -- 'active' while in flight, NULL once finished. Derived from state rather
    -- than maintained, so no code path can leave the two disagreeing.
    active_marker TEXT GENERATED ALWAYS AS (
        CASE WHEN state IN ('canary', 'awaiting_promote', 'promoting', 'blocked')
             THEN 'active' END
    ) VIRTUAL
);

-- At most one upgrade in flight, enforced by the database rather than by Go.
-- Two concurrent fleet upgrades to different versions is not a race to be
-- handled carefully, it is a state with no correct meaning.
--
-- NULLs are distinct in a SQLite unique index, so any number of finished rows
-- coexist while only one may carry the marker. Verified against the driver
-- this project actually uses.
CREATE UNIQUE INDEX idx_xray_upgrades_active ON xray_upgrades(active_marker);

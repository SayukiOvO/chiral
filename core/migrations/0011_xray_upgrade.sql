-- Runtime Xray-core upgrades: what each node was told to install, and how far
-- it got.

-- Which release asset a node needs. Reported by the agent in Hello, because it
-- is the only thing that knows; inferring it panel-side is how a node gets told
-- to install a binary for the wrong architecture.
ALTER TABLE nodes ADD COLUMN platform TEXT NOT NULL DEFAULT '';

-- One row per node: the most recent install attempt.
--
-- Not a full history. The useful questions are "what is this node doing right
-- now" and "how did the last attempt end", and both are answered by the latest
-- row; a node's version history is already legible from the audit log, which is
-- where a record of who asked for what belongs.
CREATE TABLE node_xray_installs (
    node_id      TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    version      TEXT NOT NULL,
    download_url TEXT NOT NULL,
    -- Lowercase hex SHA-256 of the archive. Stored rather than re-derived so
    -- the relay serves exactly the bytes the node was told to expect, even if
    -- upstream re-publishes the tag underneath us.
    sha256       TEXT NOT NULL,
    -- Whether the node was told to restart into it, or only to stage it.
    activate     INTEGER NOT NULL DEFAULT 1,
    -- Mirrors chiral.v1.XrayInstallPhase, stored as its name so a dump is
    -- readable and a renumbered enum cannot silently reinterpret old rows.
    phase        TEXT NOT NULL DEFAULT 'UNSPECIFIED',
    -- On failure, the kernel's own stderr — the only thing that tells an admin
    -- what to fix.
    message      TEXT NOT NULL DEFAULT '',
    started_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX idx_node_xray_installs_phase ON node_xray_installs(phase);

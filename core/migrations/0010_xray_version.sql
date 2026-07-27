-- Split "which Xray version" into the two questions it has always been.
--
-- nodes.xray_version was set once per stream, from the Hello frame, and meant
-- whatever the agent felt like reporting. That was fine while the binary never
-- changed underneath a running agent. Runtime upgrades break it in both
-- directions at once: an upgrade lands without dropping the stream, so the
-- recorded value goes stale and no Hello ever comes to refresh it; and the
-- moment a swap and a restart can be separated, one column cannot say both
-- "the new kernel is on disk" and "the new kernel is actually serving".
--
-- So: xray_version keeps its meaning as the RUNNING process's version and is
-- now refreshed from every heartbeat, while xray_installed_version records
-- what the next start would use. They agree in the steady state. They disagree
-- exactly when something is wrong, which is the case worth being able to see.

ALTER TABLE nodes ADD COLUMN xray_installed_version TEXT NOT NULL DEFAULT '';

-- Seed it from what we already know, so a node that never reconnects still
-- reads as "running what it was installed with" rather than "nothing
-- installed" — the latter would make an empty column look like a finding.
UPDATE nodes SET xray_installed_version = xray_version;

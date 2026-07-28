-- Make the node-name uniqueness the API already claims actually true.
--
-- updateNode and createNode both carry a 409 branch reading "a node with that
-- name already exists". Neither could ever run: nodes.name was declared
-- `TEXT NOT NULL` with no unique constraint, so isConflict() never fired and
-- two nodes could be given the same name — by creation or by rename, silently
-- and with a 200.
--
-- The name is the handle an operator uses everywhere: the roster, the alert
-- that wakes them at 3am, the audit trail. Two boxes answering to it is a
-- worse outcome than a refused rename.
--
-- Existing duplicates are renamed rather than rejected. A migration that fails
-- on somebody's live data is a panel that will not start, which is a far bigger
-- problem than a node briefly called "tokyo-1 (2)"; the suffix is visible and
-- the operator can set whatever they meant.
UPDATE nodes SET name = name || ' (' || (
  SELECT COUNT(*) FROM nodes AS earlier
  WHERE earlier.name = nodes.name AND earlier.created_at < nodes.created_at
) || ')'
WHERE EXISTS (
  SELECT 1 FROM nodes AS earlier
  WHERE earlier.name = nodes.name AND earlier.created_at < nodes.created_at
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_name ON nodes(name);

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

-- Existing duplicates are renamed rather than rejected. A migration that fails
-- on somebody's live data is a panel that will not start at all, which is far
-- worse than a node briefly called "tokyo-1 (a3f2…)".
--
-- The suffix is the node's own id, and that is not cosmetic. The obvious
-- version — number the duplicates by counting earlier rows with the same name
-- — was written first and crash-looped a real panel on the first rehearsal:
-- SQLite evaluates a correlated subquery against the table AS IT IS BEING
-- UPDATED, so once the second of three "hk-01" rows had become "hk-01 (1)",
-- the third counted only one remaining "hk-01" before it and became
-- "hk-01 (1)" as well. The UPDATE then collided with the index it was meant to
-- make possible.
--
-- An id suffix cannot do that: ids are unique, so every renamed row lands on a
-- name no other row can take, whatever order the update visits them in. The
-- earliest row of each group is never renamed, because nothing sorts before it.
UPDATE nodes SET name = name || ' (' || id || ')'
WHERE EXISTS (
  SELECT 1 FROM nodes AS earlier
  WHERE earlier.name = nodes.name
    AND (earlier.created_at < nodes.created_at
         OR (earlier.created_at = nodes.created_at AND earlier.id < nodes.id))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_name ON nodes(name);

-- Let an external node be dialled through another external node.
--
-- The chain has only ever pointed at the fleet, because `chain_node_id`
-- references `nodes` and there was nowhere else for it to point. That is not a
-- policy anyone chose — it is the shape of the column. An operator who buys a
-- relay from one provider and an exit from another has no fleet node in the
-- middle to name, and the one arrangement the chain exists to express is the
-- one it cannot.
--
-- A second column rather than one opaque id: the foreign keys are what make a
-- deleted node or a refreshed source clean up after itself, and a single column
-- could only keep that by dropping the reference and hoping. At most one of the
-- two is set — a proxy is dialled directly, through a fleet node, or through
-- another external node, never two of those at once.
ALTER TABLE external_proxies
    ADD COLUMN chain_proxy_id TEXT REFERENCES external_proxies (id) ON DELETE SET NULL;

-- SQLite cannot add a CHECK to an existing table without rewriting it, and
-- rewriting this one would churn every id — the ids are what per-user denials
-- and the chains themselves are keyed by. The invariant is held in the store
-- instead, which is also where the cycle check has to live: a CHECK constraint
-- can see one row and cycles are a property of the graph.
CREATE INDEX IF NOT EXISTS idx_external_proxies_chain_proxy
    ON external_proxies (chain_proxy_id);

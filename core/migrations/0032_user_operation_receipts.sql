-- Execution receipts contain no account payload or proxy secret. A pending
-- row is deliberately also the durable uncertain-outcome barrier: neither
-- reconnecting nor restarting Core may replay it under a new operation ID.
CREATE TABLE user_operation_receipts (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    inbound_tag TEXT NOT NULL,
    email TEXT NOT NULL,
    operation_id TEXT NOT NULL UNIQUE,
    kind INTEGER NOT NULL CHECK (kind IN (1, 2)),
    request_digest TEXT NOT NULL,
    result INTEGER NOT NULL DEFAULT 0 CHECK (result IN (0, 1, 2)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (node_id, inbound_tag, email)
);

-- Losing the credential would remove the context needed to resolve an
-- uncertain write. Protect both explicit deletions and foreign-key cascades
-- (including background external-subscription refreshes), not only handlers.
CREATE TRIGGER credential_pending_operation_delete
BEFORE DELETE ON credentials
WHEN EXISTS (
    SELECT 1 FROM user_operation_receipts
    WHERE node_id = OLD.node_id AND email = OLD.email AND result = 0
)
BEGIN
    SELECT RAISE(ABORT, 'credential has an unresolved user operation');
END;

CREATE TRIGGER credential_pending_operation_identity_update
BEFORE UPDATE OF id, user_id, profile_id, node_id, email, secret, exit_proxy_id, exit_relay_id ON credentials
WHEN (
    OLD.id IS NOT NEW.id OR OLD.user_id IS NOT NEW.user_id OR
    OLD.profile_id IS NOT NEW.profile_id OR OLD.node_id IS NOT NEW.node_id OR
    OLD.email IS NOT NEW.email OR OLD.secret IS NOT NEW.secret OR
    OLD.exit_proxy_id IS NOT NEW.exit_proxy_id OR OLD.exit_relay_id IS NOT NEW.exit_relay_id
) AND EXISTS (
    SELECT 1 FROM user_operation_receipts
    WHERE node_id = OLD.node_id AND email = OLD.email AND result = 0
)
BEGIN
    SELECT RAISE(ABORT, 'credential has an unresolved user operation');
END;

-- A node also owns the receipt through ON DELETE CASCADE. Its guard must run
-- before either cascade, so SQLite's child-deletion order cannot erase the
-- pending barrier before the credential guard observes it.
CREATE TRIGGER node_pending_operation_delete
BEFORE DELETE ON nodes
WHEN EXISTS (
    SELECT 1 FROM user_operation_receipts WHERE node_id = OLD.id AND result = 0
)
BEGIN
    SELECT RAISE(ABORT, 'node has an unresolved user operation');
END;

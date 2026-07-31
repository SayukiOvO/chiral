-- Panel settings an operator changes from the console.
--
-- Distinct from environment variables on purpose: those are deployment facts —
-- where the database is, which port to bind — and changing one means editing a
-- file and restarting. These are choices about what the panel produces, and an
-- operator should not have to touch the host to make them.
--
-- A key/value table rather than a column per setting: each of these is read in
-- exactly one place and there is no query that filters or joins on them.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

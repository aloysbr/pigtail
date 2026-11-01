-- Held waits (subject « mac handover », D-held): a wait kept by a process
-- (type mac, holder_pid) and finished by it, its death, the end of the session
-- or its until; never by a prompt or a turn. SQLite cannot alter a CHECK: the
-- table is rebuilt (create new, copy, drop, rename), in one transaction with
-- foreign keys off (store.go applyOne), like 0015 and 0018. 0013 and 0016
-- create no index on this table: nothing to recreate.

CREATE TABLE session_waits_0019 (
    session_id TEXT NOT NULL,
    source     TEXT NOT NULL CHECK (source IN ('auto', 'declared', 'held')),
    type       TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    since      INTEGER NOT NULL,           -- epoch ms
    until_ms   INTEGER,
    holder_pid INTEGER,
    PRIMARY KEY (session_id, source),
    CHECK (source <> 'held' OR COALESCE(holder_pid, 0) > 0)  -- a bare NULL > 0 would pass
);
INSERT INTO session_waits_0019 (session_id, source, type, reason, since, until_ms)
    SELECT session_id, source, type, reason, since, until_ms FROM session_waits;
DROP TABLE session_waits;
ALTER TABLE session_waits_0019 RENAME TO session_waits;

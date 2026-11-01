-- /prio (sub_x000000000000029, D7, D9): the session is filed in the « Prio »
-- folder at the top of the cmux sidebar. Keyed by the Claude session id; the
-- row outlives SessionEnd (a relaunch resumes the same id, like Standby D24).
-- It goes by `pigtail session prio --clear`, by entering Standby, when another
-- conversation takes the session's surface, or when its workspace is gone:
-- missing_ticks = successful cmux listings in a row without the session's
-- workspace (purge at 2, like session_standby). No deadline (D9). Additive.
CREATE TABLE IF NOT EXISTS session_prio (
    session_id    TEXT PRIMARY KEY,
    since         INTEGER NOT NULL,
    missing_ticks INTEGER NOT NULL DEFAULT 0
);

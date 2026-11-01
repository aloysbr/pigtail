CREATE TABLE IF NOT EXISTS projects (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL,
    root_path          TEXT NOT NULL,
    remote_kind        TEXT,
    remote_owner       TEXT,
    remote_repo        TEXT,
    default_profile_id TEXT,
    default_backend    TEXT NOT NULL,
    port_ranges        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT NOT NULL,
    intent             TEXT NOT NULL,
    state              TEXT NOT NULL,
    path               TEXT NOT NULL,
    ports              TEXT NOT NULL,
    recipe_id          TEXT,
    backend_kind       TEXT NOT NULL,
    backend_data       TEXT,
    created_at         INTEGER NOT NULL,
    last_activity_at   INTEGER NOT NULL,
    snoozed_until      INTEGER,
    active_delivery_id TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id)
);

CREATE INDEX IF NOT EXISTS idx_workspaces_state   ON workspaces(state);
CREATE INDEX IF NOT EXISTS idx_workspaces_project ON workspaces(project_id);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    agent_kind          TEXT NOT NULL,
    profile_id          TEXT,
    state               TEXT NOT NULL,
    tmux_session_name   TEXT,
    started_at          INTEGER NOT NULL,
    last_event_at       INTEGER NOT NULL,
    external_session_id TEXT,
    message_count       INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id)
);

CREATE INDEX IF NOT EXISTS idx_agent_sessions_workspace ON agent_sessions(workspace_id);

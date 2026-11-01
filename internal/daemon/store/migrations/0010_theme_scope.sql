-- Scope isolation. Scope lives on the theme; subjects derive it via theme_id.
-- The set of scopes is not in SQL: only the shape of a name is checked here
-- (0015 makes it a lowercase identifier).
ALTER TABLE themes ADD COLUMN scope TEXT NOT NULL DEFAULT ''
    CHECK (length(scope) <= 32);
CREATE INDEX IF NOT EXISTS idx_themes_scope ON themes(scope);

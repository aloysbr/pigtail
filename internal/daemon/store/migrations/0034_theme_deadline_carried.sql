-- sub_x000000000000003 (D1, 2026-10-28): a theme (pigtail project) may carry an
-- optional deadline (YYYY-MM-DD, '' = none) and a carried flag (a project the user
-- carries himself). The GLOB is the shape floor only; calendar validity is
-- checked in Go (pigtail.ValidThemeDeadline). Additive.
ALTER TABLE themes ADD COLUMN deadline TEXT NOT NULL DEFAULT ''
    CHECK (deadline = '' OR deadline GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]');
ALTER TABLE themes ADD COLUMN carried INTEGER NOT NULL DEFAULT 0 CHECK (carried IN (0, 1));

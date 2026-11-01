-- Adds main_worktree column to projects. This is the subdirectory name
-- inside the container that holds the main git checkout (typically "main").
-- NULL means the project uses the legacy sibling layout where RootPath IS
-- the main worktree.
ALTER TABLE projects ADD COLUMN main_worktree TEXT;

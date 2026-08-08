-- A context branch path must survive a rename: derive it from the current
-- name and renaming an agent silently orphans it from its own context.
-- So the path is frozen at creation and stored, never recomputed.
ALTER TABLE workspaces ADD COLUMN branch TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN branch TEXT NOT NULL DEFAULT '';

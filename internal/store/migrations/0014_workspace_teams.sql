-- A workspace can be shared with any number of teams.
--
-- workspaces.team_id held exactly one, which made "share it with the platform
-- team as well" impossible without taking it away from research first. The
-- column stays for now so an older binary reading this database still sees
-- the first grant, but nothing writes it any more and Visible() no longer
-- consults it.

CREATE TABLE workspace_teams (
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    team_id      INTEGER NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    created_at   TEXT    NOT NULL,
    PRIMARY KEY (workspace_id, team_id)
);

CREATE INDEX idx_workspace_teams_team ON workspace_teams (team_id);

-- Carry every existing single grant across, so nobody loses access on upgrade.
INSERT INTO workspace_teams (workspace_id, team_id, created_at)
SELECT id, team_id, datetime('now')
  FROM workspaces
 WHERE team_id IS NOT NULL;

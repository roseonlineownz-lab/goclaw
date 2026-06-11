ALTER TABLE team_tasks ADD COLUMN IF NOT EXISTS user_id VARCHAR(255);
ALTER TABLE team_tasks ADD COLUMN IF NOT EXISTS channel VARCHAR(50);
CREATE INDEX IF NOT EXISTS idx_team_tasks_user_scope ON team_tasks(team_id, user_id) WHERE user_id IS NOT NULL;

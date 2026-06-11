ALTER TABLE skills ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE skills ADD COLUMN IF NOT EXISTS deps JSONB NOT NULL DEFAULT '{}';
ALTER TABLE skills ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT true;
CREATE INDEX IF NOT EXISTS idx_skills_system ON skills(is_system) WHERE is_system = true;
CREATE INDEX IF NOT EXISTS idx_skills_enabled ON skills(enabled) WHERE enabled = false;

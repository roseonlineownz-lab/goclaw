ALTER TABLE cron_jobs ADD COLUMN IF NOT EXISTS stateless BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE cron_jobs ADD COLUMN IF NOT EXISTS deliver BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE cron_jobs ADD COLUMN IF NOT EXISTS deliver_channel TEXT NOT NULL DEFAULT '';
ALTER TABLE cron_jobs ADD COLUMN IF NOT EXISTS deliver_to TEXT NOT NULL DEFAULT '';
ALTER TABLE cron_jobs ADD COLUMN IF NOT EXISTS wake_heartbeat BOOLEAN NOT NULL DEFAULT false;

UPDATE cron_jobs SET
  deliver = COALESCE((payload->>'deliver')::boolean, false),
  deliver_channel = COALESCE(payload->>'channel', ''),
  deliver_to = COALESCE(payload->>'to', ''),
  wake_heartbeat = COALESCE((payload->>'wake_heartbeat')::boolean, false)
WHERE payload IS NOT NULL;

UPDATE cron_jobs SET payload = payload - 'deliver' - 'channel' - 'to' - 'wake_heartbeat'
WHERE payload IS NOT NULL;

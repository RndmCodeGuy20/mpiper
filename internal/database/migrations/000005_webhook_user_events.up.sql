ALTER TABLE webhook_registrations ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE webhook_registrations ADD COLUMN events JSONB NOT NULL DEFAULT '[]'::jsonb;
CREATE INDEX idx_webhook_registrations_user ON webhook_registrations (user_id);

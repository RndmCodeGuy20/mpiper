DROP INDEX IF EXISTS idx_webhook_registrations_user;
ALTER TABLE webhook_registrations DROP COLUMN events;
ALTER TABLE webhook_registrations DROP COLUMN user_id;

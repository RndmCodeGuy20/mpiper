-- Webhook secrets move from the shared ENCRYPTION_KEY to a dedicated
-- WEBHOOK_ENCRYPTION_KEY. The project is pre-launch/local, so rather than
-- re-encrypting existing rows we drop them: any secret still encrypted with the
-- old key would fail to decrypt under the new key. CASCADE also clears the
-- dependent webhook_deliveries rows.
TRUNCATE webhook_registrations CASCADE;

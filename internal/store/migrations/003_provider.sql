-- A database belongs to one notification platform. Legacy link column names
-- remain unchanged so existing Discord installations upgrade without data loss.
CREATE TABLE notification_provider (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    provider TEXT NOT NULL CHECK (provider IN ('discord', 'telegram'))
);
-- Existing linked identities are Discord IDs, never Telegram IDs.
INSERT INTO notification_provider (singleton, provider)
SELECT 1, 'discord' WHERE EXISTS (SELECT 1 FROM links);

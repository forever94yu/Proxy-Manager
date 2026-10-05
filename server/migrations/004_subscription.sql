-- Subscription URLs carry an HMAC of the user ID and this version, so no
-- token is stored. Incrementing the version revokes the current URL.
ALTER TABLE proxy_users ADD COLUMN subscription_version INTEGER NOT NULL DEFAULT 0;

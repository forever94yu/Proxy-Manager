ALTER TABLE servers ADD COLUMN config_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN remote_user_count INTEGER;
ALTER TABLE job_targets ADD COLUMN server_config_revision INTEGER NOT NULL DEFAULT 0;

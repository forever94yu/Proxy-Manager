ALTER TABLE proxy_users ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE proxy_users ADD COLUMN traffic_limit_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proxy_users ADD COLUMN expires_at TEXT;
ALTER TABLE proxy_users ADD COLUMN reset_period TEXT NOT NULL DEFAULT 'none';
ALTER TABLE proxy_users ADD COLUMN reset_anchor TEXT NOT NULL DEFAULT '';
ALTER TABLE proxy_users ADD COLUMN period_token INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proxy_users ADD COLUMN period_started_at TEXT;
ALTER TABLE proxy_users ADD COLUMN next_reset_at TEXT;
ALTER TABLE proxy_users ADD COLUMN last_reset_at TEXT;

UPDATE proxy_users SET period_started_at = created_at WHERE period_started_at IS NULL;

-- Latest node-reported traffic state per user and server. Rows outlive the
-- binding (and the server) so traffic already used in the current accounting
-- period keeps counting towards the quota; rows of past periods are pruned.
--
-- A node counts one accounting period of one account in one counter
-- (counter_index). When the node starts a new counter within the same period
-- (the account was removed and added again), the final value of the previous
-- counter moves to retained_bytes. Usage on the server in the period is
-- retained_bytes + counter_bytes.
CREATE TABLE IF NOT EXISTS proxy_user_traffic (
    user_id TEXT NOT NULL REFERENCES proxy_users(id) ON DELETE CASCADE,
    server_id TEXT NOT NULL,
    on_node INTEGER NOT NULL DEFAULT 0,
    has_policy INTEGER NOT NULL DEFAULT 0,
    node_state TEXT NOT NULL DEFAULT '',
    node_cap_mb INTEGER NOT NULL DEFAULT 0,
    period_token INTEGER,
    counter_index INTEGER NOT NULL DEFAULT 0,
    counter_bytes INTEGER NOT NULL DEFAULT 0,
    retained_bytes INTEGER NOT NULL DEFAULT 0,
    observed_at TEXT NOT NULL,
    PRIMARY KEY (user_id, server_id)
);

CREATE INDEX IF NOT EXISTS proxy_user_traffic_server_idx ON proxy_user_traffic(server_id);

ALTER TABLE servers ADD COLUMN traffic_synced_at TEXT;
ALTER TABLE servers ADD COLUMN traffic_error TEXT NOT NULL DEFAULT '';

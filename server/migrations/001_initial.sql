CREATE TABLE IF NOT EXISTS servers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    host TEXT NOT NULL,
    ssh_port INTEGER NOT NULL,
    ssh_user TEXT NOT NULL,
    auth_method TEXT NOT NULL,
    credential_cipher TEXT NOT NULL,
    host_fingerprint TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unknown',
    service_status TEXT NOT NULL DEFAULT 'unknown',
    install_status TEXT NOT NULL DEFAULT 'not_installed',
    os TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    public_ip TEXT NOT NULL DEFAULT '',
    http_port INTEGER NOT NULL,
    socks_port INTEGER NOT NULL,
    dns_json TEXT NOT NULL DEFAULT '[]',
    tags_json TEXT NOT NULL DEFAULT '[]',
    last_seen_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS servers_name_unique ON servers(name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS servers_status_idx ON servers(status);

CREATE TABLE IF NOT EXISTS proxy_users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_cipher TEXT NOT NULL,
    sync_status TEXT NOT NULL DEFAULT 'pending',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS proxy_user_servers (
    user_id TEXT NOT NULL REFERENCES proxy_users(id) ON DELETE CASCADE,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, server_id)
);

CREATE INDEX IF NOT EXISTS proxy_user_servers_server_idx ON proxy_user_servers(server_id);

CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    status TEXT NOT NULL,
    actor TEXT NOT NULL,
    entity_type TEXT NOT NULL DEFAULT '',
    entity_id TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);

CREATE INDEX IF NOT EXISTS jobs_status_created_idx ON jobs(status, created_at);
CREATE INDEX IF NOT EXISTS jobs_type_created_idx ON jobs(type, created_at);

CREATE TABLE IF NOT EXISTS job_targets (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    server_id TEXT NOT NULL,
    server_name TEXT NOT NULL,
    status TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    finished_at TEXT
);

CREATE INDEX IF NOT EXISTS job_targets_job_idx ON job_targets(job_id);
CREATE INDEX IF NOT EXISTS job_targets_status_idx ON job_targets(status);
CREATE INDEX IF NOT EXISTS job_targets_server_status_idx ON job_targets(server_id, status);

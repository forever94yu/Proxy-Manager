package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
	ErrServerBusy = errors.New("server has queued or running jobs")
	ErrStale      = errors.New("resource changed after it was read")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	db *sql.DB
}

type NewJob struct {
	ID         string
	Type       string
	Actor      string
	EntityType string
	EntityID   string
	Message    string
	Targets    []NewJobTarget
}

type NewJobTarget struct {
	ID                   string
	ServerID             string
	ServerConfigRevision int
	Payload              string
}

type ClaimedTarget struct {
	Target JobTarget
	Job    Job
}

type ExecutionUpdate struct {
	HostFingerprint string
	ServerStatus    string
	ServiceStatus   string
	InstallStatus   string
	OS              string
	Version         string
	PublicIP        string
}

func OpenStore(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	if path == ":memory:" {
		// SQLite creates a separate :memory: database per connection.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(16)
		db.SetMaxIdleConns(8)
	}
	store := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping SQLite: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", entry.Name()).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if applied != 0 {
			continue
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, string(contents)); err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", entry.Name(), formatTime(time.Now()))
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

const serverSelect = `
SELECT s.id, s.name, s.host, s.ssh_port, s.ssh_user, s.auth_method,
       s.credential_cipher, s.host_fingerprint, s.config_revision, s.status, s.service_status,
       s.install_status, s.os, s.version, s.public_ip, s.http_port, s.socks_port,
       s.dns_json, s.tags_json, s.last_seen_at, s.created_at, s.updated_at,
	   COALESCE(s.remote_user_count, (SELECT COUNT(*) FROM proxy_user_servers pus WHERE pus.server_id = s.id)),
       (SELECT COUNT(*) FROM proxy_user_servers pus WHERE pus.server_id = s.id)
FROM servers s`

func (s *Store) ListServers(ctx context.Context, search, status string) ([]Server, error) {
	query := serverSelect + " WHERE 1=1"
	args := make([]any, 0, 3)
	if search != "" {
		query += " AND (s.name LIKE ? ESCAPE '\\' OR s.host LIKE ? ESCAPE '\\' OR s.tags_json LIKE ? ESCAPE '\\')"
		pattern := "%" + escapeLike(search) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	if status != "" {
		switch status {
		case "online", "offline", "unknown":
			query += " AND s.status = ?"
			args = append(args, status)
		case "running", "stopped":
			query += " AND s.service_status = ?"
			args = append(args, status)
		case "installed", "deploying", "not_installed":
			query += " AND s.install_status = ?"
			args = append(args, status)
		case "failed":
			query += " AND (s.service_status = 'failed' OR s.install_status = 'failed')"
		}
	}
	query += " ORDER BY s.name COLLATE NOCASE"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer rows.Close()
	servers := make([]Server, 0)
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, rows.Err()
}

func (s *Store) GetServer(ctx context.Context, id string) (Server, error) {
	server, err := scanServer(s.db.QueryRowContext(ctx, serverSelect+" WHERE s.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	return server, err
}

func (s *Store) CreateServer(ctx context.Context, server Server) error {
	dnsJSON, _ := json.Marshal(server.DNS)
	tagsJSON, _ := json.Marshal(server.Tags)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO servers (
    id, name, host, ssh_port, ssh_user, auth_method, credential_cipher,
    status, service_status, install_status, public_ip, http_port, socks_port,
    dns_json, tags_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		server.ID, server.Name, server.Host, server.SSHPort, server.SSHUser,
		server.AuthMethod, server.CredentialCipher, server.Status, server.ServiceStatus,
		server.InstallStatus, server.PublicIP, server.HTTPPort, server.SocksPort,
		string(dnsJSON), string(tagsJSON), formatTime(server.CreatedAt), formatTime(server.UpdatedAt))
	if isSQLiteUniqueError(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	return nil
}

func (s *Store) UpdateServer(ctx context.Context, server Server, replaceCredential bool) error {
	dnsJSON, _ := json.Marshal(server.DNS)
	tagsJSON, _ := json.Marshal(server.Tags)
	query := `
UPDATE servers SET
    name = ?, host = ?, ssh_port = ?, ssh_user = ?, auth_method = ?,
	public_ip = ?, http_port = ?, socks_port = ?, dns_json = ?, tags_json = ?, updated_at = ?,
	config_revision = config_revision + 1,
	host_fingerprint = CASE WHEN host <> ? OR ssh_port <> ? THEN '' ELSE host_fingerprint END,
	remote_user_count = CASE WHEN host <> ? OR ssh_port <> ? THEN NULL ELSE remote_user_count END`
	args := []any{
		server.Name, server.Host, server.SSHPort, server.SSHUser, server.AuthMethod,
		server.PublicIP, server.HTTPPort, server.SocksPort, string(dnsJSON), string(tagsJSON), formatTime(server.UpdatedAt),
		server.Host, server.SSHPort, server.Host, server.SSHPort,
	}
	if replaceCredential {
		query += ", credential_cipher = ?"
		args = append(args, server.CredentialCipher)
	}
	query += ` WHERE id = ? AND NOT EXISTS (
    SELECT 1 FROM job_targets
    WHERE server_id = ? AND status IN ('queued', 'running')
)`
	args = append(args, server.ID, server.ID)
	result, err := s.db.ExecContext(ctx, query, args...)
	if isSQLiteUniqueError(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("update server: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM servers WHERE id = ?", server.ID).Scan(&exists); err != nil {
		return fmt.Errorf("check server after conditional update: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrServerBusy
}

func (s *Store) DeleteServer(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
DELETE FROM servers
WHERE id = ?
  AND NOT EXISTS (
      SELECT 1 FROM job_targets
      WHERE server_id = ? AND status IN ('queued', 'running')
  )`, id, id)
	if err != nil {
		return fmt.Errorf("delete server: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM servers WHERE id = ?", id).Scan(&exists); err != nil {
		return fmt.Errorf("check server after conditional delete: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrConflict
}

func (s *Store) SetServerInstallStatus(ctx context.Context, id, status string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE servers SET install_status = ?, updated_at = ? WHERE id = ?`, status, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("set server install status: %w", err)
	}
	return requireAffected(result)
}

func scanServer(scanner interface{ Scan(...any) error }) (Server, error) {
	var server Server
	var dnsJSON, tagsJSON string
	var lastSeen sql.NullString
	var createdAt, updatedAt string
	err := scanner.Scan(
		&server.ID, &server.Name, &server.Host, &server.SSHPort, &server.SSHUser,
		&server.AuthMethod, &server.CredentialCipher, &server.HostFingerprint, &server.ConfigRevision,
		&server.Status, &server.ServiceStatus, &server.InstallStatus, &server.OS,
		&server.Version, &server.PublicIP, &server.HTTPPort, &server.SocksPort,
		&dnsJSON, &tagsJSON, &lastSeen, &createdAt, &updatedAt, &server.UserCount, &server.DesiredUserCount,
	)
	if err != nil {
		return Server{}, err
	}
	if err := json.Unmarshal([]byte(dnsJSON), &server.DNS); err != nil {
		return Server{}, fmt.Errorf("decode server DNS: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &server.Tags); err != nil {
		return Server{}, fmt.Errorf("decode server tags: %w", err)
	}
	server.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Server{}, err
	}
	server.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Server{}, err
	}
	if lastSeen.Valid {
		value, err := parseTime(lastSeen.String)
		if err != nil {
			return Server{}, err
		}
		server.LastSeenAt = &value
	}
	if server.DNS == nil {
		server.DNS = []string{}
	}
	if server.Tags == nil {
		server.Tags = []string{}
	}
	return server, nil
}

func (s *Store) ListProxyUsers(ctx context.Context, search, syncStatus string) ([]ProxyUser, error) {
	query := `
SELECT id, username, password_cipher, sync_status, created_at, updated_at
FROM proxy_users WHERE 1=1`
	args := make([]any, 0, 2)
	if search != "" {
		query += " AND username LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(search)+"%")
	}
	if syncStatus != "" {
		query += " AND sync_status = ?"
		args = append(args, syncStatus)
	}
	query += " ORDER BY username COLLATE NOCASE"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list proxy users: %w", err)
	}
	defer rows.Close()
	users := make([]ProxyUser, 0)
	for rows.Next() {
		var user ProxyUser
		var createdAt, updatedAt string
		if err := rows.Scan(&user.ID, &user.Username, &user.PasswordCipher, &user.SyncStatus, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		user.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		user.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range users {
		users[index].ServerIDs, err = s.userServerIDs(ctx, users[index].ID)
		if err != nil {
			return nil, err
		}
		users[index].ServerCount = len(users[index].ServerIDs)
	}
	return users, nil
}

func (s *Store) GetProxyUser(ctx context.Context, id string) (ProxyUser, error) {
	var user ProxyUser
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT id, username, password_cipher, sync_status, created_at, updated_at
FROM proxy_users WHERE id = ?`, id).Scan(
		&user.ID, &user.Username, &user.PasswordCipher, &user.SyncStatus, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProxyUser{}, ErrNotFound
	}
	if err != nil {
		return ProxyUser{}, fmt.Errorf("get proxy user: %w", err)
	}
	user.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return ProxyUser{}, err
	}
	user.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return ProxyUser{}, err
	}
	user.ServerIDs, err = s.userServerIDs(ctx, id)
	if err != nil {
		return ProxyUser{}, err
	}
	user.ServerCount = len(user.ServerIDs)
	return user, nil
}

func (s *Store) userServerIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT server_id FROM proxy_user_servers WHERE user_id = ? ORDER BY server_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) CreateProxyUser(ctx context.Context, user ProxyUser, job NewJob) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
INSERT INTO proxy_users(id, username, password_cipher, sync_status, created_at, updated_at)
VALUES (?, ?, ?, 'pending', ?, ?)`, user.ID, user.Username, user.PasswordCipher,
		formatTime(user.CreatedAt), formatTime(user.UpdatedAt))
	if isSQLiteUniqueError(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("create proxy user: %w", err)
	}
	for _, serverID := range user.ServerIDs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO proxy_user_servers(user_id, server_id) VALUES (?, ?)`, user.ID, serverID); err != nil {
			if isSQLiteForeignKeyError(err) {
				return ErrNotFound
			}
			return fmt.Errorf("bind proxy user to server: %w", err)
		}
	}
	if err := insertJobTx(ctx, tx, job); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateProxyUser(ctx context.Context, user ProxyUser, expectedUpdatedAt time.Time, job NewJob) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE proxy_users SET username = ?, password_cipher = ?, sync_status = 'pending', updated_at = ?
WHERE id = ? AND updated_at = ?`,
		user.Username, user.PasswordCipher, formatTime(user.UpdatedAt), user.ID, formatTime(expectedUpdatedAt))
	if isSQLiteUniqueError(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("update proxy user: %w", err)
	}
	if err := classifyConditionalUserTx(ctx, tx, result, user.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_user_servers WHERE user_id = ?", user.ID); err != nil {
		return fmt.Errorf("replace proxy user bindings: %w", err)
	}
	for _, serverID := range user.ServerIDs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO proxy_user_servers(user_id, server_id) VALUES (?, ?)`, user.ID, serverID); err != nil {
			if isSQLiteForeignKeyError(err) {
				return ErrNotFound
			}
			return fmt.Errorf("bind proxy user to server: %w", err)
		}
	}
	if err := insertJobTx(ctx, tx, job); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteProxyUser(ctx context.Context, id string, expectedUpdatedAt time.Time, job NewJob) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertJobTx(ctx, tx, job); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM proxy_users WHERE id = ? AND updated_at = ?", id, formatTime(expectedUpdatedAt))
	if err != nil {
		return fmt.Errorf("delete proxy user: %w", err)
	}
	if err := classifyConditionalUserTx(ctx, tx, result, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteProxyUserWithoutJob(ctx context.Context, id string, expectedUpdatedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM proxy_users WHERE id = ? AND updated_at = ?", id, formatTime(expectedUpdatedAt))
	if err != nil {
		return fmt.Errorf("delete unbound proxy user: %w", err)
	}
	if err := classifyConditionalUserTx(ctx, tx, result, id); err != nil {
		return err
	}
	return tx.Commit()
}

func classifyConditionalUserTx(ctx context.Context, tx *sql.Tx, result sql.Result, id string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM proxy_users WHERE id = ?", id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrStale
}

func (s *Store) CreateJob(ctx context.Context, job NewJob) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertJobTx(ctx, tx, job); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateDeploymentJob(ctx context.Context, job NewJob) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertJobTx(ctx, tx, job); err != nil {
		return err
	}
	now := formatTime(time.Now())
	for _, target := range job.Targets {
		result, err := tx.ExecContext(ctx, `
UPDATE servers SET install_status = 'deploying', updated_at = ? WHERE id = ?`, now, target.ServerID)
		if err != nil {
			return err
		}
		if err := requireAffected(result); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertJobTx(ctx context.Context, tx *sql.Tx, job NewJob) error {
	if len(job.Targets) == 0 {
		return errors.New("job requires at least one target")
	}
	now := formatTime(time.Now())
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(id, type, status, actor, entity_type, entity_id, message, created_at)
VALUES (?, ?, 'queued', ?, ?, ?, ?, ?)`, job.ID, job.Type, job.Actor,
		job.EntityType, job.EntityID, job.Message, now)
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	for _, target := range job.Targets {
		var serverName string
		var serverConfigRevision int
		if err := tx.QueryRowContext(ctx, "SELECT name, config_revision FROM servers WHERE id = ?", target.ServerID).Scan(&serverName, &serverConfigRevision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("resolve job server: %w", err)
		}
		if serverConfigRevision != target.ServerConfigRevision {
			return ErrStale
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO job_targets(id, job_id, server_id, server_name, status, payload_json, server_config_revision)
VALUES (?, ?, ?, ?, 'queued', ?, ?)`, target.ID, job.ID, target.ServerID, serverName, target.Payload, target.ServerConfigRevision); err != nil {
			return fmt.Errorf("create job target: %w", err)
		}
	}
	return nil
}

func (s *Store) ListJobs(ctx context.Context, status, jobType string, limit int) ([]Job, error) {
	query := jobSelect + " WHERE 1=1"
	args := make([]any, 0, 3)
	if status != "" {
		query += " AND j.status = ?"
		args = append(args, status)
	}
	if jobType != "" {
		query += " AND j.type = ?"
		args = append(args, jobType)
	}
	query += " GROUP BY j.id ORDER BY j.created_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

const jobSelect = `
SELECT j.id, j.type, j.status, j.actor, j.entity_type, j.entity_id, j.message,
       j.created_at, j.started_at, j.finished_at,
       COUNT(t.id),
       COALESCE(SUM(CASE WHEN t.status = 'succeeded' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN t.status = 'failed' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN t.status IN ('succeeded', 'failed', 'cancelled') THEN 1 ELSE 0 END), 0)
FROM jobs j LEFT JOIN job_targets t ON t.job_id = j.id`

func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx, jobSelect+" WHERE j.id = ? GROUP BY j.id", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return job, err
}

func (s *Store) ListJobTargets(ctx context.Context, jobID string) ([]JobTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_id, server_id, server_name, status, attempt, error, started_at, finished_at
FROM job_targets
WHERE job_id = ?
ORDER BY rowid`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list job targets: %w", err)
	}
	defer rows.Close()

	targets := make([]JobTarget, 0)
	for rows.Next() {
		var target JobTarget
		var startedAt, finishedAt sql.NullString
		if err := rows.Scan(
			&target.ID, &target.JobID, &target.ServerID, &target.ServerName,
			&target.Status, &target.Attempt, &target.Error, &startedAt, &finishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan job target: %w", err)
		}
		target.Error = sanitizeMessage(target.Error)
		if startedAt.Valid {
			value, err := parseTime(startedAt.String)
			if err != nil {
				return nil, err
			}
			target.StartedAt = &value
		}
		if finishedAt.Valid {
			value, err := parseTime(finishedAt.String)
			if err != nil {
				return nil, err
			}
			target.FinishedAt = &value
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list job targets: %w", err)
	}
	return targets, nil
}

func scanJob(scanner interface{ Scan(...any) error }) (Job, error) {
	var job Job
	var createdAt string
	var startedAt, finishedAt sql.NullString
	var doneCount int
	err := scanner.Scan(&job.ID, &job.Type, &job.Status, &job.Actor, &job.EntityType,
		&job.EntityID, &job.Message, &createdAt, &startedAt, &finishedAt,
		&job.TargetCount, &job.SuccessCount, &job.FailedCount, &doneCount)
	if err != nil {
		return Job{}, err
	}
	job.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Job{}, err
	}
	if startedAt.Valid {
		value, err := parseTime(startedAt.String)
		if err != nil {
			return Job{}, err
		}
		job.StartedAt = &value
	}
	if finishedAt.Valid {
		value, err := parseTime(finishedAt.String)
		if err != nil {
			return Job{}, err
		}
		job.FinishedAt = &value
	}
	if job.TargetCount > 0 {
		job.Progress = doneCount * 100 / job.TargetCount
	}
	return job, nil
}

func (s *Store) RetryJob(ctx context.Context, id string) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var jobType, status, entityType, entityID string
	var rowID int64
	if err := tx.QueryRowContext(ctx, `
SELECT rowid, type, status, entity_type, entity_id FROM jobs WHERE id = ?`, id).Scan(&rowID, &jobType, &status, &entityType, &entityID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	if status != "failed" && status != "partially_failed" {
		return Job{}, ErrConflict
	}
	var newerTargets int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM job_targets original
JOIN job_targets newer_target ON newer_target.server_id = original.server_id
JOIN jobs newer_job ON newer_job.id = newer_target.job_id
WHERE original.job_id = ?
  AND original.status = 'failed'
  AND newer_job.rowid > ?`, id, rowID).Scan(&newerTargets); err != nil {
		return Job{}, err
	}
	if newerTargets > 0 {
		return Job{}, ErrConflict
	}
	var changedTargets int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM job_targets target
LEFT JOIN servers server ON server.id = target.server_id
WHERE target.job_id = ?
  AND target.status = 'failed'
  AND (server.id IS NULL OR server.config_revision <> target.server_config_revision)`, id).Scan(&changedTargets); err != nil {
		return Job{}, err
	}
	if changedTargets > 0 {
		return Job{}, ErrConflict
	}
	if entityType == "proxy_user" && entityID != "" && jobType != "user_delete" {
		var entityExists int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM proxy_users WHERE id = ?`, entityID).Scan(&entityExists); err != nil {
			return Job{}, err
		}
		if entityExists == 0 {
			return Job{}, ErrConflict
		}
	}
	result, err := tx.ExecContext(ctx, `
UPDATE job_targets SET status = 'queued', error = '', started_at = NULL, finished_at = NULL
WHERE job_id = ? AND status = 'failed'`, id)
	if err != nil {
		return Job{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return Job{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs SET status = 'queued', finished_at = NULL, message = 'Retry queued' WHERE id = ?`, id); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.GetJob(ctx, id)
}

func (s *Store) RecoverInterruptedJobs(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE job_targets SET status = 'queued', started_at = NULL, error = 'Worker restarted before completion'
WHERE status = 'running'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs SET status = 'queued', started_at = NULL, message = 'Recovered after worker restart'
WHERE status = 'running'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimTarget(ctx context.Context) (*ClaimedTarget, error) {
	for attempts := 0; attempts < 4; attempts++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		var claimed ClaimedTarget
		err = tx.QueryRowContext(ctx, `
SELECT t.id, t.job_id, t.server_id, t.server_name, t.status, t.payload_json, t.attempt, t.error,
       j.id, j.type, j.status, j.actor, j.entity_type, j.entity_id, j.message, j.created_at, j.started_at, j.finished_at
FROM job_targets t
JOIN jobs j ON j.id = t.job_id
WHERE t.status = 'queued'
  AND j.status IN ('queued', 'running')
  AND NOT EXISTS (
      SELECT 1 FROM job_targets active
      WHERE active.server_id = t.server_id AND active.status = 'running'
  )
ORDER BY j.created_at, t.id
LIMIT 1`).Scan(
			&claimed.Target.ID, &claimed.Target.JobID, &claimed.Target.ServerID,
			&claimed.Target.ServerName, &claimed.Target.Status, &claimed.Target.Payload,
			&claimed.Target.Attempt, &claimed.Target.Error,
			&claimed.Job.ID, &claimed.Job.Type, &claimed.Job.Status, &claimed.Job.Actor,
			&claimed.Job.EntityType, &claimed.Job.EntityID, &claimed.Job.Message,
			new(string), new(sql.NullString), new(sql.NullString),
		)
		if errors.Is(err, sql.ErrNoRows) {
			tx.Rollback()
			return nil, nil
		}
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		now := formatTime(time.Now())
		result, err := tx.ExecContext(ctx, `
UPDATE job_targets SET status = 'running', attempt = attempt + 1, started_at = ?, finished_at = NULL
WHERE id = ? AND status = 'queued'`, now, claimed.Target.ID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			tx.Rollback()
			continue
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE jobs SET status = 'running', started_at = COALESCE(started_at, ?), finished_at = NULL
WHERE id = ?`, now, claimed.Job.ID); err != nil {
			tx.Rollback()
			return nil, err
		}
		if claimed.Job.EntityType == "proxy_user" {
			_, _ = tx.ExecContext(ctx, "UPDATE proxy_users SET sync_status = 'pending' WHERE id = ?", claimed.Job.EntityID)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		claimed.Target.Status = "running"
		claimed.Target.Attempt++
		return &claimed, nil
	}
	return nil, nil
}

func (s *Store) CompleteTarget(ctx context.Context, claimed ClaimedTarget, success bool, message string, update ExecutionUpdate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	status := "succeeded"
	targetError := ""
	if !success {
		status = "failed"
		targetError = sanitizeMessage(message)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE job_targets SET status = ?, error = ?, finished_at = ? WHERE id = ? AND status = 'running'`,
		status, targetError, now, claimed.Target.ID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		var existingStatus string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM job_targets WHERE id = ?", claimed.Target.ID).Scan(&existingStatus); err != nil {
			return err
		}
		if existingStatus == status {
			return nil
		}
		return ErrConflict
	}
	if err := applyExecutionUpdateTx(ctx, tx, claimed.Target.ServerID, success, update, now); err != nil {
		return err
	}
	if err := refreshJobTx(ctx, tx, claimed.Job.ID, message, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecoverStaleTargets(ctx context.Context, before time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT job_id
FROM job_targets
WHERE status = 'running' AND started_at IS NOT NULL AND started_at < ?`, formatTime(before))
	if err != nil {
		return err
	}
	jobIDs := make([]string, 0)
	for rows.Next() {
		var jobID string
		if err := rows.Scan(&jobID); err != nil {
			rows.Close()
			return err
		}
		jobIDs = append(jobIDs, jobID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(jobIDs) == 0 {
		return tx.Commit()
	}
	now := formatTime(time.Now())
	if _, err := tx.ExecContext(ctx, `
UPDATE job_targets
SET status = 'failed', finished_at = ?,
    error = 'Automatic reconciliation attempts exhausted'
WHERE status = 'running' AND started_at IS NOT NULL AND started_at < ? AND attempt >= 3`, now, formatTime(before)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_targets
SET status = 'queued', started_at = NULL, finished_at = NULL,
    error = 'Worker lost the completion acknowledgement; reconciliation queued'
WHERE status = 'running' AND started_at IS NOT NULL AND started_at < ? AND attempt < 3`, formatTime(before)); err != nil {
		return err
	}
	for _, jobID := range jobIDs {
		if err := refreshJobTx(ctx, tx, jobID, "Automatic reconciliation attempts exhausted", now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = 'queued', finished_at = NULL, message = 'Stale target reconciliation queued'
WHERE id IN (`+placeholders(len(jobIDs))+`)
  AND NOT EXISTS (
      SELECT 1 FROM job_targets
      WHERE job_targets.job_id = jobs.id AND job_targets.status = 'running'
  )
  AND EXISTS (
      SELECT 1 FROM job_targets
      WHERE job_targets.job_id = jobs.id AND job_targets.status = 'queued'
	  )`, stringArgs(jobIDs)...); err != nil {
		return err
	}
	return tx.Commit()
}

func placeholders(count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = "?"
	}
	return strings.Join(values, ",")
}

func stringArgs(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func applyExecutionUpdateTx(ctx context.Context, tx *sql.Tx, serverID string, success bool, update ExecutionUpdate, now string) error {
	if update.HostFingerprint != "" {
		result, err := tx.ExecContext(ctx, `
UPDATE servers SET host_fingerprint = ? WHERE id = ? AND host_fingerprint = ''`, update.HostFingerprint, serverID)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			var existing string
			if err := tx.QueryRowContext(ctx, "SELECT host_fingerprint FROM servers WHERE id = ?", serverID).Scan(&existing); err != nil {
				return err
			}
			if existing != update.HostFingerprint {
				return errors.New("SSH host fingerprint changed during first-use enrollment")
			}
		}
	}
	if success {
		_, err := tx.ExecContext(ctx, `
UPDATE servers SET
    status = CASE WHEN ? <> '' THEN ? ELSE status END,
    service_status = CASE WHEN ? <> '' THEN ? ELSE service_status END,
    install_status = CASE WHEN ? <> '' THEN ? ELSE install_status END,
    os = CASE WHEN ? <> '' THEN ? ELSE os END,
    version = CASE WHEN ? <> '' THEN ? ELSE version END,
    public_ip = CASE WHEN ? <> '' THEN ? ELSE public_ip END,
    last_seen_at = ?, updated_at = ?
WHERE id = ?`,
			update.ServerStatus, update.ServerStatus,
			update.ServiceStatus, update.ServiceStatus,
			update.InstallStatus, update.InstallStatus,
			update.OS, update.OS, update.Version, update.Version,
			update.PublicIP, update.PublicIP, now, now, serverID)
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE servers SET
	status = CASE WHEN ? <> '' THEN ? ELSE status END,
    service_status = CASE WHEN ? <> '' THEN ? ELSE service_status END,
    install_status = CASE WHEN ? <> '' THEN ? ELSE install_status END,
    updated_at = ?
WHERE id = ?`, update.ServerStatus, update.ServerStatus,
		update.ServiceStatus, update.ServiceStatus,
		update.InstallStatus, update.InstallStatus,
		now, serverID)
	return err
}

func refreshJobTx(ctx context.Context, tx *sql.Tx, jobID, lastMessage, now string) error {
	var total, queued, running, succeeded, failed int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN status = 'queued' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0)
FROM job_targets WHERE job_id = ?`, jobID).Scan(&total, &queued, &running, &succeeded, &failed); err != nil {
		return err
	}
	jobStatus := "running"
	finishedAt := any(nil)
	message := "Operation is running"
	if queued == 0 && running == 0 {
		finishedAt = now
		switch {
		case succeeded == total:
			jobStatus = "succeeded"
			message = "Operation completed"
		case failed == total:
			jobStatus = "failed"
			message = sanitizeMessage(lastMessage)
		default:
			jobStatus = "partially_failed"
			message = fmt.Sprintf("Completed on %d of %d servers", succeeded, total)
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs SET status = ?, message = ?, finished_at = ? WHERE id = ?`,
		jobStatus, message, finishedAt, jobID); err != nil {
		return err
	}
	var entityType, entityID string
	if err := tx.QueryRowContext(ctx, "SELECT entity_type, entity_id FROM jobs WHERE id = ?", jobID).Scan(&entityType, &entityID); err != nil {
		return err
	}
	if entityType == "proxy_user" {
		syncStatus := "pending"
		switch jobStatus {
		case "succeeded":
			syncStatus = "synced"
		case "partially_failed":
			syncStatus = "partial"
		case "failed":
			syncStatus = "failed"
		}
		_, _ = tx.ExecContext(ctx, "UPDATE proxy_users SET sync_status = ?, updated_at = ? WHERE id = ?", syncStatus, now, entityID)
	}
	return nil
}

func (s *Store) Dashboard(ctx context.Context) (Dashboard, error) {
	var dashboard Dashboard
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN status = 'online' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN service_status = 'running' THEN 1 ELSE 0 END), 0)
FROM servers`).Scan(&dashboard.Stats.TotalServers, &dashboard.Stats.OnlineServers, &dashboard.Stats.RunningServices); err != nil {
		return Dashboard{}, err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM proxy_users").Scan(&dashboard.Stats.TotalUsers); err != nil {
		return Dashboard{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM jobs WHERE status IN ('failed', 'partially_failed')`).Scan(&dashboard.Stats.FailedJobs); err != nil {
		return Dashboard{}, err
	}
	servers, err := s.ListServers(ctx, "", "")
	if err != nil {
		return Dashboard{}, err
	}
	if len(servers) > 8 {
		servers = servers[:8]
	}
	jobs, err := s.ListJobs(ctx, "", "", 8)
	if err != nil {
		return Dashboard{}, err
	}
	dashboard.Servers = servers
	dashboard.RecentJobs = jobs
	return dashboard, nil
}

func (s *Store) ServerNames(ctx context.Context, ids []string) (map[string]string, error) {
	result := make(map[string]string, len(ids))
	for _, id := range ids {
		var name string
		if err := s.db.QueryRowContext(ctx, "SELECT name FROM servers WHERE id = ?", id).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		result[id] = name
	}
	return result, nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func requireAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func isSQLiteUniqueError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

func isSQLiteForeignKeyError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed")
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse database timestamp: %w", err)
	}
	return parsed, nil
}

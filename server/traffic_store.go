package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PolicyInput is everything needed to compute the desired node policy of one
// user on one server.
type PolicyInput struct {
	ServerID string
	User     ProxyUser
	Observed ObservedPolicy
}

const observedColumns = `t.observed_at, t.on_node, t.has_policy, t.node_state, t.node_cap_mb, t.period_token, t.counter_bytes`

type observedRow struct {
	observedAt   sql.NullString
	onNode       sql.NullInt64
	hasPolicy    sql.NullInt64
	state        sql.NullString
	capMB        sql.NullInt64
	period       sql.NullInt64
	counterBytes sql.NullInt64
}

func (r *observedRow) targets() []any {
	return []any{&r.observedAt, &r.onNode, &r.hasPolicy, &r.state, &r.capMB, &r.period, &r.counterBytes}
}

func (r *observedRow) finish() (ObservedPolicy, error) {
	if !r.observedAt.Valid {
		return ObservedPolicy{}, nil
	}
	observedAt, err := parseTime(r.observedAt.String)
	if err != nil {
		return ObservedPolicy{}, err
	}
	return ObservedPolicy{
		Present:      true,
		OnNode:       r.onNode.Int64 != 0,
		HasPolicy:    r.hasPolicy.Int64 != 0,
		State:        r.state.String,
		CapMB:        r.capMB.Int64,
		Period:       r.period.Int64,
		PeriodValid:  r.period.Valid,
		CounterBytes: r.counterBytes.Int64,
		ObservedAt:   observedAt,
	}, nil
}

// ServerPolicyInputs returns the policy inputs of every user bound to the
// server, or of every binding when serverID is empty.
func (s *Store) ServerPolicyInputs(ctx context.Context, serverID string, now time.Time) ([]PolicyInput, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT pus.server_id, `+proxyUserColumns+`, `+observedColumns+`
FROM proxy_user_servers pus
JOIN proxy_users u ON u.id = pus.user_id
LEFT JOIN proxy_user_traffic t ON t.user_id = pus.user_id AND t.server_id = pus.server_id
WHERE ? = '' OR pus.server_id = ?
ORDER BY pus.server_id, u.username COLLATE NOCASE`, serverID, serverID)
	if err != nil {
		return nil, fmt.Errorf("list policy inputs: %w", err)
	}
	defer rows.Close()
	inputs := make([]PolicyInput, 0)
	for rows.Next() {
		var input PolicyInput
		var user proxyUserRow
		var observed observedRow
		targets := append([]any{&input.ServerID}, user.targets()...)
		if err := rows.Scan(append(targets, observed.targets()...)...); err != nil {
			return nil, err
		}
		if input.User, err = user.finish(now); err != nil {
			return nil, err
		}
		if input.Observed, err = observed.finish(); err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

// UserPolicyInput returns the policy input of one user on one server whether
// or not the user is still bound to it. ok is false when the user no longer
// exists.
func (s *Store) UserPolicyInput(ctx context.Context, userID, serverID string, now time.Time) (PolicyInput, bool, error) {
	var user proxyUserRow
	var observed observedRow
	err := s.db.QueryRowContext(ctx, `
SELECT `+proxyUserColumns+`, `+observedColumns+`
FROM proxy_users u
LEFT JOIN proxy_user_traffic t ON t.user_id = u.id AND t.server_id = ?
WHERE u.id = ?`, serverID, userID).Scan(append(user.targets(), observed.targets()...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return PolicyInput{}, false, nil
	}
	if err != nil {
		return PolicyInput{}, false, fmt.Errorf("get policy input: %w", err)
	}
	input := PolicyInput{ServerID: serverID}
	if input.User, err = user.finish(now); err != nil {
		return PolicyInput{}, false, err
	}
	if input.Observed, err = observed.finish(); err != nil {
		return PolicyInput{}, false, err
	}
	return input, true, nil
}

// trafficRow is the stored accounting state of one user on one server.
type trafficRow struct {
	Exists        bool
	OnNode        bool
	HasPolicy     bool
	State         string
	CapMB         int64
	Period        sql.NullInt64
	CounterIndex  int
	CounterBytes  int64
	RetainedBytes int64
	ObservedAt    time.Time
}

func (r *trafficRow) samePeriod(period int64) bool {
	return r.Period.Valid && r.Period.Int64 == period
}

func (r *trafficRow) startPeriod(period int64) {
	r.Period = sql.NullInt64{Int64: period, Valid: true}
	r.CounterIndex, r.CounterBytes, r.RetainedBytes = 0, 0, 0
}

// closeCounter keeps the last known value of a counter that no longer exists
// on the node, so usage of the period survives account re-creation.
func (r *trafficRow) closeCounter() {
	r.RetainedBytes += r.CounterBytes
	r.CounterIndex, r.CounterBytes = 0, 0
}

// applyLive records a live policy entry. Counters only grow, so a different
// counter index, a value below the previous one, or a counter that was
// reported removed means the node started a fresh counter within the period.
func (r *trafficRow) applyLive(entry TrafficEntry) {
	if !r.samePeriod(entry.Period) {
		r.startPeriod(entry.Period)
	}
	if r.State == NodeStateRemoved || r.CounterIndex != entry.Index || entry.Bytes < r.CounterBytes {
		r.closeCounter()
		r.CounterIndex = entry.Index
	}
	r.CounterBytes = entry.Bytes
	r.OnNode, r.HasPolicy = true, true
	r.State, r.CapMB = entry.State, entry.CapMB
}

// applyRemoved records the final value of a counter whose account was removed
// from the node. The counter stays attached to the row, marked final, so a
// duplicate report of the same counter is not counted twice.
func (r *trafficRow) applyRemoved(entry TrafficEntry) {
	if !r.samePeriod(entry.Period) {
		r.startPeriod(entry.Period)
	}
	if r.CounterIndex == entry.Index {
		r.CounterBytes = max(r.CounterBytes, entry.Bytes)
	} else {
		r.closeCounter()
		r.CounterIndex, r.CounterBytes = entry.Index, entry.Bytes
	}
	r.OnNode, r.HasPolicy = false, false
	r.State = NodeStateRemoved
}

// applyAbsent records that the node has no policy for the user. The counter is
// kept: an account renamed after this observation reappears with the same
// counter, and a re-created account gets a new one that applyLive detects.
func (r *trafficRow) applyAbsent(onNode bool) {
	r.OnNode, r.HasPolicy = onNode, false
	if r.State != NodeStateRemoved {
		r.State = ""
	}
}

// applyTrafficReportTx merges a node traffic report into proxy_user_traffic.
// Only users bound to the server, or with accounting rows on it, are
// considered; unknown node accounts are ignored. Rows that already hold a newer
// observation are left alone.
func applyTrafficReportTx(ctx context.Context, tx *sql.Tx, serverID string, report *TrafficReport) error {
	candidates := make(map[string]string)
	rows, err := tx.QueryContext(ctx, `
SELECT u.id, u.username FROM proxy_users u
WHERE u.id IN (SELECT user_id FROM proxy_user_servers WHERE server_id = ?)
   OR u.id IN (SELECT user_id FROM proxy_user_traffic WHERE server_id = ?)`, serverID, serverID)
	if err != nil {
		return fmt.Errorf("list traffic candidates: %w", err)
	}
	for rows.Next() {
		var id, username string
		if err := rows.Scan(&id, &username); err != nil {
			rows.Close()
			return err
		}
		candidates[username] = id
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(candidates) == 0 {
		return markTrafficSyncedTx(ctx, tx, serverID, report.ObservedAt)
	}

	existing := make(map[string]trafficRow)
	rows, err = tx.QueryContext(ctx, `
SELECT user_id, on_node, has_policy, node_state, node_cap_mb, period_token,
       counter_index, counter_bytes, retained_bytes, observed_at
FROM proxy_user_traffic WHERE server_id = ?`, serverID)
	if err != nil {
		return fmt.Errorf("read traffic rows: %w", err)
	}
	for rows.Next() {
		var userID, observedAt string
		var onNode, hasPolicy int
		row := trafficRow{Exists: true}
		if err := rows.Scan(&userID, &onNode, &hasPolicy, &row.State, &row.CapMB, &row.Period,
			&row.CounterIndex, &row.CounterBytes, &row.RetainedBytes, &observedAt); err != nil {
			rows.Close()
			return err
		}
		row.OnNode, row.HasPolicy = onNode != 0, hasPolicy != 0
		if row.ObservedAt, err = parseTime(observedAt); err != nil {
			rows.Close()
			return err
		}
		existing[userID] = row
	}
	if err := rows.Close(); err != nil {
		return err
	}

	nodeUsers := stringSet(report.NodeUsers)
	live := make(map[string]TrafficEntry)
	removed := make(map[string][]TrafficEntry)
	for _, entry := range report.Entries {
		if entry.State == NodeStateRemoved {
			removed[entry.Username] = append(removed[entry.Username], entry)
		} else {
			live[entry.Username] = entry
		}
	}

	for username, userID := range candidates {
		row := existing[userID]
		if row.Exists && row.ObservedAt.After(report.ObservedAt) {
			continue
		}
		for _, entry := range removed[username] {
			row.applyRemoved(entry)
		}
		_, onNode := nodeUsers[username]
		if entry, ok := live[username]; ok {
			row.applyLive(entry)
		} else if _, wasRemoved := removed[username]; !wasRemoved || onNode {
			row.applyAbsent(onNode)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO proxy_user_traffic(
    user_id, server_id, on_node, has_policy, node_state, node_cap_mb, period_token,
    counter_index, counter_bytes, retained_bytes, observed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user_id, server_id) DO UPDATE SET
    on_node = excluded.on_node, has_policy = excluded.has_policy, node_state = excluded.node_state,
    node_cap_mb = excluded.node_cap_mb, period_token = excluded.period_token,
    counter_index = excluded.counter_index, counter_bytes = excluded.counter_bytes,
    retained_bytes = excluded.retained_bytes, observed_at = excluded.observed_at`,
			userID, serverID, boolInt(row.OnNode), boolInt(row.HasPolicy), row.State, row.CapMB, row.Period,
			row.CounterIndex, row.CounterBytes, row.RetainedBytes, formatTime(report.ObservedAt)); err != nil {
			return fmt.Errorf("store traffic row: %w", err)
		}
	}
	return markTrafficSyncedTx(ctx, tx, serverID, report.ObservedAt)
}

func markTrafficSyncedTx(ctx context.Context, tx *sql.Tx, serverID string, observedAt time.Time) error {
	var current sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT traffic_synced_at FROM servers WHERE id = ?", serverID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	syncedAt := observedAt
	if previous, err := parseNullTime(current); err == nil && previous != nil && previous.After(syncedAt) {
		syncedAt = *previous
	}
	_, err := tx.ExecContext(ctx, "UPDATE servers SET traffic_synced_at = ?, traffic_error = '' WHERE id = ?",
		formatTime(syncedAt), serverID)
	return err
}

// RecordTrafficSync stores the outcome of a background traffic collection.
// Failures are recorded without touching the server's connectivity state.
func (s *Store) RecordTrafficSync(ctx context.Context, serverID string, report *TrafficReport, syncErr error) error {
	if syncErr != nil {
		_, err := s.db.ExecContext(ctx, "UPDATE servers SET traffic_error = ? WHERE id = ?",
			sanitizeMessage(syncErr.Error()), serverID)
		return err
	}
	if report == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := applyTrafficReportTx(ctx, tx, serverID, report); err != nil {
		return err
	}
	return tx.Commit()
}

// ProcessDueResets starts a new accounting period for every user whose
// periodic reset is due, catching up missed boundaries with a single reset.
func (s *Store) ProcessDueResets(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, reset_period, reset_anchor, next_reset_at FROM proxy_users WHERE reset_period <> 'none'`)
	if err != nil {
		return 0, fmt.Errorf("list periodic users: %w", err)
	}
	type candidate struct {
		id, period, anchor string
		next               sql.NullString
	}
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.period, &item.anchor, &item.next); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	resets := 0
	for _, item := range candidates {
		anchor, err := parseUsageTime(item.anchor)
		if err != nil || !validResetPeriod(item.period) {
			continue
		}
		nextAt, err := parseNullTime(item.next)
		if err != nil {
			return resets, err
		}
		next := nextResetBoundary(item.period, anchor, now)
		if nextAt == nil {
			// Schedule without a pending boundary: arm it without resetting.
			if _, err := s.db.ExecContext(ctx, `
UPDATE proxy_users SET next_reset_at = ? WHERE id = ? AND next_reset_at IS NULL`, formatTime(next), item.id); err != nil {
				return resets, err
			}
			continue
		}
		if now.Before(*nextAt) {
			continue
		}
		start := currentPeriodStart(item.period, anchor, now)
		result, err := s.db.ExecContext(ctx, `
UPDATE proxy_users SET period_token = period_token + 1, period_started_at = ?, last_reset_at = ?, next_reset_at = ?
WHERE id = ? AND next_reset_at = ?`, formatTime(start), formatTime(start), formatTime(next), item.id, item.next.String)
		if err != nil {
			return resets, fmt.Errorf("reset proxy user traffic: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			resets++
		}
	}
	return resets, nil
}

// PruneTraffic removes accounting rows that no longer count towards any quota:
// rows of servers the user is not bound to, from a past accounting period.
func (s *Store) PruneTraffic(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM proxy_user_traffic
WHERE NOT EXISTS (
    SELECT 1 FROM proxy_user_servers pus
    WHERE pus.user_id = proxy_user_traffic.user_id AND pus.server_id = proxy_user_traffic.server_id
)
AND (
    period_token IS NULL
    OR period_token <> (SELECT u.period_token FROM proxy_users u WHERE u.id = proxy_user_traffic.user_id)
)`)
	if err != nil {
		return fmt.Errorf("prune traffic rows: %w", err)
	}
	return nil
}

// EnqueuePolicyJob queues a system policy synchronization for one server
// unless doing so would pile up work: the server must be idle, the effect of
// the previous user job must have been observed, and a recently failed policy
// synchronization backs off.
func (s *Store) EnqueuePolicyJob(ctx context.Context, server Server, now time.Time, failureBackoff time.Duration) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM job_targets WHERE server_id = ? AND status IN ('queued', 'running')`, server.ID).Scan(&active); err != nil {
		return "", err
	}
	if active > 0 {
		return "", nil
	}
	var lastUserJob, syncedAt sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT j.created_at FROM job_targets t JOIN jobs j ON j.id = t.job_id
WHERE t.server_id = ? AND j.type LIKE 'user\_%' ESCAPE '\'
ORDER BY j.rowid DESC LIMIT 1`, server.ID).Scan(&lastUserJob)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, "SELECT traffic_synced_at FROM servers WHERE id = ?", server.ID).Scan(&syncedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if lastUserJob.Valid {
		lastJobAt, err := parseTime(lastUserJob.String)
		if err != nil {
			return "", err
		}
		synced, err := parseNullTime(syncedAt)
		if err != nil {
			return "", err
		}
		if synced == nil || !synced.After(lastJobAt) {
			return "", nil
		}
	}
	var recentFailures int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM job_targets t JOIN jobs j ON j.id = t.job_id
WHERE t.server_id = ? AND j.type = 'user_policy' AND t.status = 'failed' AND t.finished_at >= ?`,
		server.ID, formatTime(now.Add(-failureBackoff))).Scan(&recentFailures); err != nil {
		return "", err
	}
	if recentFailures > 0 {
		return "", nil
	}
	jobID := newID()
	targets, err := makeTargets([]Server{server}, func(string) TargetTask { return TargetTask{Action: "policy-apply"} })
	if err != nil {
		return "", err
	}
	if err := insertJobTx(ctx, tx, NewJob{
		ID: jobID, Type: "user_policy", Actor: "system", EntityType: "server", EntityID: server.ID,
		Message: "Proxy user policy synchronization queued", Targets: targets,
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

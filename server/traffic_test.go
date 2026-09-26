package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingExecutor wraps the mock executor and records every request.
type recordingExecutor struct {
	mock     *MockExecutor
	mu       sync.Mutex
	requests []ExecutionRequest
}

func (e *recordingExecutor) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	e.mu.Lock()
	e.requests = append(e.requests, request)
	e.mu.Unlock()
	return e.mock.Execute(ctx, request)
}

func (e *recordingExecutor) last(action string) (ExecutionRequest, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for index := len(e.requests) - 1; index >= 0; index-- {
		if e.requests[index].Task.Action == action {
			return e.requests[index], true
		}
	}
	return ExecutionRequest{}, false
}

type userMutationEnvelope struct {
	Data struct {
		User ProxyUser `json:"user"`
		Job  *Job      `json:"job"`
	} `json:"data"`
}

func installTestServer(t *testing.T, app *testApplication, name string) Server {
	t.Helper()
	server := createTestServer(t, app, name, name+".example.com", "ssh-secret")
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/servers/"+server.ID+"/deploy", map[string]any{})
	assertStatus(t, response, http.StatusAccepted)
	var envelope struct {
		Data struct {
			Job Job `json:"job"`
		} `json:"data"`
	}
	decodeBody(t, readBody(t, response), &envelope)
	if job := waitForJob(t, app, envelope.Data.Job.ID); job.Status != "succeeded" {
		t.Fatalf("deploy failed: %+v", job)
	}
	installed, err := app.store.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

func mutateUser(t *testing.T, app *testApplication, method, path string, body any, status int) userMutationEnvelope {
	t.Helper()
	response := requestJSON(t, app.client, method, app.server.URL+path, body)
	assertStatus(t, response, status)
	var envelope userMutationEnvelope
	decodeBody(t, readBody(t, response), &envelope)
	if envelope.Data.Job != nil {
		if job := waitForJob(t, app, envelope.Data.Job.ID); job.Status != "succeeded" {
			t.Fatalf("job %s failed: %+v", job.Type, job)
		}
	}
	return envelope
}

func TestAPIUserUsageLimitsLifecycle(t *testing.T) {
	recorder := &recordingExecutor{mock: &MockExecutor{Delay: time.Millisecond}}
	app := newTestApplication(t, recorder)
	loginTestClient(t, app)
	server := installTestServer(t, app, "quota")

	// Validation of the usage fields.
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users", map[string]any{
		"username": "invalid_limits", "passwordMode": "generated", "serverIds": []string{server.ID},
		"trafficLimitBytes": -1, "expiresAt": "tomorrow", "resetPeriod": "hourly",
	})
	assertStatus(t, response, http.StatusUnprocessableEntity)
	body := readBody(t, response)
	for _, field := range []string{"trafficLimitBytes", "expiresAt", "resetPeriod"} {
		if !strings.Contains(body, field) {
			t.Fatalf("missing field error %s in %s", field, body)
		}
	}
	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users", map[string]any{
		"username": "missing_anchor", "passwordMode": "generated", "serverIds": []string{server.ID},
		"resetPeriod": "monthly",
	})
	assertStatus(t, response, http.StatusUnprocessableEntity)
	if body := readBody(t, response); !strings.Contains(body, "Reset anchor is required") {
		t.Fatalf("unexpected body: %s", body)
	}

	// Defaults when the usage fields are omitted.
	plain := mutateUser(t, app, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "plain_user", "passwordMode": "generated", "serverIds": []string{server.ID},
	}, http.StatusAccepted).Data.User
	if !plain.Enabled || plain.TrafficLimitBytes != 0 || plain.ExpiresAt != nil || plain.ResetPeriod != ResetNone || plain.Status != UserStatusActive {
		t.Fatalf("unexpected defaults: %+v", plain)
	}
	add, ok := recorder.last("user-add")
	if !ok || add.Task.Policy == nil || add.Task.Policy.CapMB != UnlimitedCapMB || add.Task.Policy.State != NodeStateEnabled {
		t.Fatalf("user-add did not carry the unlimited policy: %+v", add.Task)
	}

	// Create with a quota, an expiry and a monthly reset.
	expiresAt := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	created := mutateUser(t, app, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "quota_user", "passwordMode": "generated", "serverIds": []string{server.ID},
		"trafficLimitBytes": 100 * mebibyte, "expiresAt": expiresAt.Format(time.RFC3339),
		"resetPeriod": "monthly", "resetAnchor": "2026-01-31T10:00:00+08:00",
	}, http.StatusAccepted).Data.User
	if created.TrafficLimitBytes != 100*mebibyte || created.ExpiresAt == nil || !created.ExpiresAt.Equal(expiresAt) ||
		created.ResetPeriod != ResetMonthly || created.ResetAnchor != "2026-01-31T10:00:00+08:00" || created.NextResetAt == nil {
		t.Fatalf("usage settings were not stored: %+v", created)
	}
	if !created.NextResetAt.After(time.Now()) || created.NextResetAt.In(time.FixedZone("", 8*3600)).Hour() != 10 {
		t.Fatalf("unexpected next reset: %s", created.NextResetAt)
	}
	add, _ = recorder.last("user-add")
	if add.Task.Username != "quota_user" || add.Task.Policy == nil || add.Task.Policy.CapMB != 100 || add.Task.Policy.Period != 0 {
		t.Fatalf("user-add policy = %+v", add.Task.Policy)
	}

	// Simulate usage on the node, then collect it.
	recorder.mock.SimulatedTrafficBytes = 40 * mebibyte
	traffic := NewTrafficManager(app.store, app.box, recorder, app.worker, Config{WorkerConcurrency: 2}, discardLogger())
	traffic.CollectOnce(context.Background())
	user, err := app.store.GetProxyUser(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if user.TrafficUsedBytes != 40*mebibyte || user.Status != UserStatusActive || user.TrafficUpdatedAt == nil {
		t.Fatalf("collected usage: %+v", user)
	}

	// Editing the quota keeps the usage of the period.
	updated := mutateUser(t, app, http.MethodPut, "/api/v1/users/"+created.ID, map[string]any{
		"username": "quota_user", "passwordMode": "unchanged", "serverIds": []string{server.ID},
		"trafficLimitBytes": 50 * mebibyte,
	}, http.StatusAccepted).Data.User
	if updated.TrafficUsedBytes != 40*mebibyte || updated.TrafficLimitBytes != 50*mebibyte || updated.ResetPeriod != ResetMonthly || updated.ExpiresAt == nil {
		t.Fatalf("edit changed usage or omitted fields: %+v", updated)
	}
	update, _ := recorder.last("user-update")
	if update.Task.Policy == nil || update.Task.Policy.CapMB != 50 {
		t.Fatalf("user-update policy = %+v", update.Task.Policy)
	}

	// Exhaust the quota: the next collection reports the user as exhausted
	// and reconciliation disables it on the node exactly once.
	traffic.CollectOnce(context.Background())
	user, _ = app.store.GetProxyUser(context.Background(), created.ID)
	if user.Status != UserStatusExhausted || user.TrafficUsedBytes != 50*mebibyte {
		t.Fatalf("expected exhausted user: %+v", user)
	}
	queued, err := traffic.ReconcileOnce(context.Background(), time.Now())
	if err != nil || len(queued) != 1 {
		t.Fatalf("reconcile queued %v, err %v", queued, err)
	}
	if job := waitForJob(t, app, queued[0]); job.Status != "succeeded" || job.Type != "user_policy" {
		t.Fatalf("policy job: %+v", job)
	}
	apply, _ := recorder.last("policy-apply")
	if policy := findPolicy(apply.Task.Policies, "quota_user"); policy == nil || policy.State != NodeStateDisabled {
		t.Fatalf("policy-apply did not disable the user: %+v", apply.Task.Policies)
	}
	if policy := findPolicy(apply.Task.Policies, "plain_user"); policy == nil || policy.State != NodeStateEnabled {
		t.Fatalf("policy-apply lost the other user: %+v", apply.Task.Policies)
	}
	if queued, err := traffic.ReconcileOnce(context.Background(), time.Now()); err != nil || len(queued) != 0 {
		t.Fatalf("converged state queued %v, err %v", queued, err)
	}

	// A manual reset starts a new period and re-enables the user.
	reset := mutateUser(t, app, http.MethodPost, "/api/v1/users/"+created.ID+"/traffic/reset", map[string]any{}, http.StatusAccepted)
	if reset.Data.Job == nil || reset.Data.Job.Type != "user_traffic_reset" {
		t.Fatalf("reset job: %+v", reset.Data.Job)
	}
	if user := reset.Data.User; user.TrafficUsedBytes != 0 || user.Status != UserStatusActive || user.PeriodStartedAt == nil || user.LastResetAt == nil {
		t.Fatalf("reset user: %+v", user)
	}
	apply, _ = recorder.last("policy-apply")
	if policy := findPolicy(apply.Task.Policies, "quota_user"); policy == nil || policy.State != NodeStateEnabled || policy.Period != 1 || policy.CapMB != 50 {
		t.Fatalf("policy after reset: %+v", policy)
	}

	// Disable and enable.
	disabled := mutateUser(t, app, http.MethodPost, "/api/v1/users/"+created.ID+"/state", map[string]any{"enabled": false}, http.StatusAccepted)
	if disabled.Data.User.Status != UserStatusDisabled || disabled.Data.Job == nil || disabled.Data.Job.Type != "user_disable" {
		t.Fatalf("disable: %+v", disabled.Data)
	}
	apply, _ = recorder.last("policy-apply")
	if policy := findPolicy(apply.Task.Policies, "quota_user"); policy == nil || policy.State != NodeStateDisabled {
		t.Fatalf("policy after disable: %+v", policy)
	}
	enabled := mutateUser(t, app, http.MethodPost, "/api/v1/users/"+created.ID+"/state", map[string]any{"enabled": true}, http.StatusAccepted)
	if enabled.Data.User.Status != UserStatusActive || enabled.Data.Job.Type != "user_enable" {
		t.Fatalf("enable: %+v", enabled.Data)
	}
	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users/"+created.ID+"/state", map[string]any{})
	assertStatus(t, response, http.StatusUnprocessableEntity)
	response.Body.Close()

	// Status filter.
	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/users?status=bogus", nil)
	assertStatus(t, response, http.StatusUnprocessableEntity)
	response.Body.Close()
	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/users?status=active", nil)
	assertStatus(t, response, http.StatusOK)
	var list struct {
		Data []ProxyUser `json:"data"`
	}
	decodeBody(t, readBody(t, response), &list)
	if len(list.Data) != 2 {
		t.Fatalf("active users = %d, want 2", len(list.Data))
	}
}

func TestAPIUsageActionsWithoutServers(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	server := installTestServer(t, app, "short-lived")
	created := mutateUser(t, app, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "unbound", "passwordMode": "generated", "serverIds": []string{server.ID},
	}, http.StatusAccepted).Data.User
	response := requestJSON(t, app.client, http.MethodDelete, app.server.URL+"/api/v1/servers/"+server.ID, nil)
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()

	reset := mutateUser(t, app, http.MethodPost, "/api/v1/users/"+created.ID+"/traffic/reset", map[string]any{}, http.StatusOK)
	if reset.Data.Job != nil || reset.Data.User.PeriodToken != 0 || reset.Data.User.LastResetAt == nil {
		t.Fatalf("reset without servers: %+v", reset.Data)
	}
	state := mutateUser(t, app, http.MethodPost, "/api/v1/users/"+created.ID+"/state", map[string]any{"enabled": false}, http.StatusOK)
	if state.Data.Job != nil || state.Data.User.Status != UserStatusDisabled {
		t.Fatalf("disable without servers: %+v", state.Data)
	}
}

func TestExpiryIsEnforcedByReconciliation(t *testing.T) {
	recorder := &recordingExecutor{mock: &MockExecutor{Delay: time.Millisecond}}
	app := newTestApplication(t, recorder)
	loginTestClient(t, app)
	server := installTestServer(t, app, "expiry")
	created := mutateUser(t, app, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "short_term", "passwordMode": "generated", "serverIds": []string{server.ID},
		"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339),
	}, http.StatusAccepted).Data.User

	traffic := NewTrafficManager(app.store, app.box, recorder, app.worker, Config{WorkerConcurrency: 1}, discardLogger())
	if queued, err := traffic.ReconcileOnce(context.Background(), time.Now()); err != nil || len(queued) != 0 {
		t.Fatalf("active user queued %v, err %v", queued, err)
	}
	// Let the expiry pass (the worker resolves policies at the real time).
	if _, err := app.store.db.Exec("UPDATE proxy_users SET expires_at = ? WHERE id = ?",
		formatTime(time.Now().Add(-time.Second)), created.ID); err != nil {
		t.Fatal(err)
	}
	// Until the account job's effect has been observed, nothing is queued.
	if _, err := app.store.db.Exec("UPDATE servers SET traffic_synced_at = NULL WHERE id = ?", server.ID); err != nil {
		t.Fatal(err)
	}
	if queued, err := traffic.ReconcileOnce(context.Background(), time.Now()); err != nil || len(queued) != 0 {
		t.Fatalf("queued before observation: %v, err %v", queued, err)
	}
	traffic.CollectOnce(context.Background())
	queued, err := traffic.ReconcileOnce(context.Background(), time.Now())
	if err != nil || len(queued) != 1 {
		t.Fatalf("expired user queued %v, err %v", queued, err)
	}
	waitForJob(t, app, queued[0])
	apply, _ := recorder.last("policy-apply")
	if policy := findPolicy(apply.Task.Policies, "short_term"); policy == nil || policy.State != NodeStateDisabled {
		t.Fatalf("expired user was not disabled: %+v", apply.Task.Policies)
	}
	// The job's own report shows the converged state.
	if queued, err := traffic.ReconcileOnce(context.Background(), time.Now()); err != nil || len(queued) != 0 {
		t.Fatalf("re-queued after convergence: %v, err %v", queued, err)
	}
	user, _ := app.store.GetProxyUser(context.Background(), created.ID)
	if user.Status != UserStatusExpired {
		t.Fatalf("status = %s, want expired", user.Status)
	}
}

func TestDueResetStartsNewPeriod(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	server := installTestServer(t, app, "periodic")
	anchor := time.Now().Add(-time.Hour).Truncate(time.Second)
	created := mutateUser(t, app, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "daily_user", "passwordMode": "generated", "serverIds": []string{server.ID},
		"resetPeriod": "daily", "resetAnchor": anchor.Format(time.RFC3339),
	}, http.StatusAccepted).Data.User
	if created.NextResetAt == nil || !created.NextResetAt.Equal(anchor.Add(24*time.Hour)) {
		t.Fatalf("next reset = %v", created.NextResetAt)
	}
	resets, err := app.store.ProcessDueResets(context.Background(), created.NextResetAt.Add(49*time.Hour))
	if err != nil || resets != 1 {
		t.Fatalf("resets = %d, err %v", resets, err)
	}
	user, _ := app.store.GetProxyUser(context.Background(), created.ID)
	if user.PeriodToken != 1 || !user.NextResetAt.Equal(anchor.Add(96*time.Hour)) || !user.PeriodStartedAt.Equal(anchor.Add(72*time.Hour)) {
		t.Fatalf("after catch-up reset: token %d next %s start %s", user.PeriodToken, user.NextResetAt, user.PeriodStartedAt)
	}
	if resets, _ := app.store.ProcessDueResets(context.Background(), user.NextResetAt.Add(-time.Second)); resets != 0 {
		t.Fatalf("reset before the boundary")
	}
}

func TestTrafficReportKeepsUsageOfRemovedAccounts(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	first := installTestServer(t, app, "first")
	second := installTestServer(t, app, "second")
	created := mutateUser(t, app, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "mover", "passwordMode": "generated", "serverIds": []string{first.ID, second.ID},
		"trafficLimitBytes": 1000 * mebibyte,
	}, http.StatusAccepted).Data.User
	observedAt := time.Now().UTC()
	if err := app.store.RecordTrafficSync(context.Background(), first.ID, &TrafficReport{
		ObservedAt: observedAt, NodeUsers: []string{"mover"},
		Entries: []TrafficEntry{{Username: "mover", State: NodeStateEnabled, CapMB: 1000, Period: 0, Index: 1, Bytes: 300 * mebibyte}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	// Unbind the first server; the node reports the final counter value.
	mutateUser(t, app, http.MethodPut, "/api/v1/users/"+created.ID, map[string]any{
		"username": "mover", "passwordMode": "unchanged", "serverIds": []string{second.ID},
	}, http.StatusAccepted)
	if err := app.store.RecordTrafficSync(context.Background(), first.ID, &TrafficReport{
		ObservedAt: time.Now().UTC(), NodeUsers: []string{},
		Entries: []TrafficEntry{{Username: "mover", State: NodeStateRemoved, CapMB: 1000, Period: 0, Index: 1, Bytes: 320 * mebibyte}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := app.store.PruneTraffic(context.Background()); err != nil {
		t.Fatal(err)
	}
	user, _ := app.store.GetProxyUser(context.Background(), created.ID)
	if user.TrafficUsedBytes != 320*mebibyte {
		t.Fatalf("usage after unbinding = %d MiB, want 320", user.TrafficUsedBytes/mebibyte)
	}
	// The remaining node may only use what is left of the global quota.
	input, ok, err := app.store.UserPolicyInput(context.Background(), created.ID, second.ID, time.Now())
	if err != nil || !ok {
		t.Fatalf("policy input: %v %v", ok, err)
	}
	if policy := desiredNodePolicy(input.User, input.Observed, time.Now()); policy.CapMB != 680 {
		t.Fatalf("cap on remaining node = %d, want 680", policy.CapMB)
	}
	// After a reset, the old period no longer counts and the row is pruned.
	mutateUser(t, app, http.MethodPost, "/api/v1/users/"+created.ID+"/traffic/reset", map[string]any{}, http.StatusAccepted)
	if err := app.store.PruneTraffic(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := app.store.db.QueryRow("SELECT COUNT(*) FROM proxy_user_traffic WHERE server_id = ?", first.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("stale rows of the unbound server were not pruned")
	}
}

func TestRetryIgnoresSystemPolicyJobs(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	good := installTestServer(t, app, "retry-good")
	bad := createTestServer(t, app, "retry-bad", "fail.example.com", "ssh-secret")
	envelope := mutateUserNoWait(t, app, map[string]any{
		"username": "retry_user", "passwordMode": "generated", "serverIds": []string{good.ID, bad.ID},
	})
	job := waitForJob(t, app, envelope.Data.Job.ID)
	if job.Status != "partially_failed" {
		t.Fatalf("job status %s", job.Status)
	}
	// A system policy synchronization on the failed server does not block
	// retrying the account creation.
	if err := app.store.CreateJob(context.Background(), NewJob{
		ID: newID(), Type: "user_policy", Actor: "system", EntityType: "server", EntityID: bad.ID,
		Targets: []NewJobTarget{{ID: newID(), ServerID: bad.ID, ServerConfigRevision: bad.ConfigRevision, Payload: `{"action":"policy-apply"}`}},
	}); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/jobs/"+job.ID+"/retry", map[string]any{})
	assertStatus(t, response, http.StatusAccepted)
	response.Body.Close()
}

func mutateUserNoWait(t *testing.T, app *testApplication, body map[string]any) userMutationEnvelope {
	t.Helper()
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users", body)
	assertStatus(t, response, http.StatusAccepted)
	var envelope userMutationEnvelope
	decodeBody(t, readBody(t, response), &envelope)
	return envelope
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func findPolicy(policies []NodePolicy, username string) *NodePolicy {
	for index := range policies {
		if policies[index].Username == username {
			return &policies[index]
		}
	}
	return nil
}

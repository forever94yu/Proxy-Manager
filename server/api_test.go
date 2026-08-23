package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type secretEchoExecutor struct{}

func (secretEchoExecutor) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	if strings.HasPrefix(request.Server.Host, "fail.") {
		return ExecutionResult{}, fmt.Errorf("remote rejected credential %s and password %s", request.Credential, request.Password)
	}
	return (&MockExecutor{Delay: time.Millisecond}).Execute(ctx, request)
}

type blockingExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *blockingExecutor) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	select {
	case e.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ExecutionResult{}, ctx.Err()
	case <-e.release:
	}
	return (&MockExecutor{Delay: time.Millisecond}).Execute(ctx, request)
}

type testApplication struct {
	store  *Store
	box    *SecretBox
	worker *Worker
	server *httptest.Server
	client *http.Client
	cancel context.CancelFunc
}

func newTestApplication(t *testing.T, executor Executor) *testApplication {
	t.Helper()
	cfg := Config{
		Environment:       "test",
		DatabasePath:      filepath.Join(t.TempDir(), "test.db"),
		AdminUsername:     "admin",
		AdminPassword:     developmentAdminPassword,
		SessionSecret:     []byte(developmentSessionSecret),
		MasterKey:         []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL:        time.Hour,
		ExecutorMode:      "mock",
		WorkerConcurrency: 2,
		WorkerPoll:        5 * time.Millisecond,
		AllowedOrigins:    map[string]struct{}{},
		StaticDir:         filepath.Join(t.TempDir(), "missing-static"),
	}
	store, err := OpenStore(cfg.DatabasePath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	box, err := NewSecretBox(cfg.MasterKey)
	if err != nil {
		t.Fatalf("NewSecretBox: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if executor == nil {
		executor = &MockExecutor{Delay: time.Millisecond}
	}
	worker := NewWorker(store, box, executor, cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("worker.Start: %v", err)
	}
	sessions := NewSessionManager(cfg)
	api := NewAPI(cfg, store, box, sessions, worker, logger)
	testServer := httptest.NewServer(api.Handler())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	application := &testApplication{
		store: store, box: box, worker: worker, server: testServer,
		client: &http.Client{Jar: jar}, cancel: cancel,
	}
	t.Cleanup(func() {
		testServer.Close()
		cancel()
		worker.Wait()
		store.Close()
	})
	return application
}

func TestAPIAuthenticationValidationAndSecretHandling(t *testing.T) {
	app := newTestApplication(t, nil)

	response := requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/dashboard", nil)
	assertStatus(t, response, http.StatusUnauthorized)
	response.Body.Close()

	request, err := http.NewRequest(http.MethodPost, app.server.URL+"/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, response, http.StatusUnsupportedMediaType)
	response.Body.Close()

	request, err = http.NewRequest(http.MethodPost, app.server.URL+"/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, response, http.StatusForbidden)
	response.Body.Close()

	request, err = http.NewRequest(http.MethodPost, app.server.URL+"/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", app.server.URL)
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, response, http.StatusUnauthorized)
	response.Body.Close()

	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "wrong-password",
	})
	assertStatus(t, response, http.StatusUnauthorized)
	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/auth/login", map[string]any{
		"username": "admin", "password": developmentAdminPassword,
	})
	assertStatus(t, response, http.StatusOK)

	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/servers", map[string]any{
		"name": "bad", "host": "server.example.com", "sshPort": 22, "sshUser": "root",
		"authMethod": "password", "credential": "secret", "httpPort": 3128,
		"socksPort": 1080, "dns": []string{"1.1.1.1"}, "tags": []string{}, "unexpected": true,
	})
	assertStatus(t, response, http.StatusBadRequest)

	const sshSecret = "ssh-password-not-for-json"
	server := createTestServer(t, app, "primary", "proxy.example.com", sshSecret)
	if server.Status != "unknown" || server.InstallStatus != "not_installed" {
		t.Fatalf("unexpected initial server state: %+v", server)
	}
	var encryptedCredential string
	if err := app.store.db.QueryRow("SELECT credential_cipher FROM servers WHERE id = ?", server.ID).Scan(&encryptedCredential); err != nil {
		t.Fatalf("read encrypted credential: %v", err)
	}
	if encryptedCredential == sshSecret || strings.Contains(encryptedCredential, sshSecret) {
		t.Fatal("SSH credential was stored in plaintext")
	}

	response = requestJSON(t, app.client, http.MethodPut, app.server.URL+"/api/v1/servers/"+server.ID, map[string]any{
		"name": "primary", "host": "proxy.example.com", "sshPort": 22, "sshUser": "root",
		"authMethod": "password", "httpPort": 3128, "socksPort": 1080,
		"dns": []string{"1.1.1.1", "1.0.0.1"}, "tags": []string{"生产", "华东"},
	})
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
	updatedServer, err := app.store.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedServer.AuthMethod != "password" || updatedServer.CredentialCipher != encryptedCredential {
		t.Fatal("empty edit credential did not preserve authentication configuration")
	}
	if len(updatedServer.Tags) != 2 {
		t.Fatalf("Unicode tags were not persisted: %v", updatedServer.Tags)
	}
	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/servers?search=%E5%8D%8E%E4%B8%9C", nil)
	assertStatus(t, response, http.StatusOK)
	if body := readBody(t, response); !strings.Contains(body, server.ID) {
		t.Fatalf("tag search omitted server: %s", body)
	}
	response = requestJSON(t, app.client, http.MethodPut, app.server.URL+"/api/v1/servers/"+server.ID, map[string]any{
		"name": "primary", "host": "proxy.example.com", "sshPort": 22, "sshUser": "root",
		"authMethod": "key", "httpPort": 3128, "socksPort": 1080,
		"dns": []string{"1.1.1.1", "1.0.0.1"}, "tags": []string{"生产", "华东"},
	})
	assertStatus(t, response, http.StatusUnprocessableEntity)
	response.Body.Close()

	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/servers/"+server.ID+"/deploy", map[string]any{})
	assertStatus(t, response, http.StatusAccepted)
	deployBody := readBody(t, response)
	var deployEnvelope struct {
		Data struct {
			Job Job `json:"job"`
		} `json:"data"`
	}
	decodeBody(t, deployBody, &deployEnvelope)
	if job := waitForJob(t, app, deployEnvelope.Data.Job.ID); job.Status != "succeeded" {
		t.Fatalf("deploy job failed: %+v", job)
	}
	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/servers?status=running", nil)
	assertStatus(t, response, http.StatusOK)
	runningBody := readBody(t, response)
	if !strings.Contains(runningBody, server.ID) {
		t.Fatalf("running service filter omitted deployed server: %s", runningBody)
	}

	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/servers", nil)
	body := readBody(t, response)
	if strings.Contains(body, sshSecret) || strings.Contains(body, "credentialCipher") || strings.Contains(body, "credential") {
		t.Fatalf("server list leaked credential material: %s", body)
	}

	const proxyPassword = "ProxyPass123"
	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users", map[string]any{
		"username": "alice", "passwordMode": "custom", "password": proxyPassword,
		"serverIds": []string{server.ID},
	})
	assertStatus(t, response, http.StatusAccepted)
	body = readBody(t, response)
	if strings.Contains(body, proxyPassword) || strings.Contains(body, "passwordCipher") {
		t.Fatalf("user creation leaked a custom password: %s", body)
	}
	var createEnvelope struct {
		Data struct {
			User ProxyUser `json:"user"`
			Job  Job       `json:"job"`
		} `json:"data"`
	}
	decodeBody(t, body, &createEnvelope)
	completed := waitForJob(t, app, createEnvelope.Data.Job.ID)
	if completed.Status != "succeeded" {
		t.Fatalf("user job status = %s, want succeeded", completed.Status)
	}
	var encryptedPassword string
	if err := app.store.db.QueryRow("SELECT password_cipher FROM proxy_users WHERE id = ?", createEnvelope.Data.User.ID).Scan(&encryptedPassword); err != nil {
		t.Fatalf("read encrypted proxy password: %v", err)
	}
	if encryptedPassword == proxyPassword || strings.Contains(encryptedPassword, proxyPassword) {
		t.Fatal("proxy password was stored in plaintext")
	}

	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/users", nil)
	body = readBody(t, response)
	if strings.Contains(body, proxyPassword) || strings.Contains(body, "password") || strings.Contains(body, "Cipher") {
		t.Fatalf("user list leaked password material: %s", body)
	}
}

func TestAPINormalizesBracketedIPv6ForDeployment(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	server := createTestServer(t, app, "ipv6", "[2001:db8::1]", "ipv6-secret")
	stored, err := app.store.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Host != "[2001:db8::1]" || stored.PublicIP != "2001:db8::1" {
		t.Fatalf("unexpected IPv6 normalization: host=%q publicIP=%q", stored.Host, stored.PublicIP)
	}
	response := requestJSON(t, app.client, http.MethodPut, app.server.URL+"/api/v1/servers/"+server.ID, map[string]any{
		"name": "ipv6", "host": "[2001:db8::2]", "sshPort": 22, "sshUser": "root",
		"authMethod": "password", "httpPort": 3128, "socksPort": 1080,
		"dns": []string{"1.1.1.1"}, "tags": []string{},
	})
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
	stored, err = app.store.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Host != "[2001:db8::2]" || stored.PublicIP != "2001:db8::2" {
		t.Fatalf("unexpected updated IPv6 normalization: host=%q publicIP=%q", stored.Host, stored.PublicIP)
	}
}

func TestAPIRejectsServerUpdateDuringActiveJob(t *testing.T) {
	executor := &blockingExecutor{started: make(chan struct{}, 1), release: make(chan struct{})}
	app := newTestApplication(t, executor)
	t.Cleanup(func() {
		select {
		case <-executor.release:
		default:
			close(executor.release)
		}
	})
	loginTestClient(t, app)
	server := createTestServer(t, app, "busy", "busy.example.com", "busy-secret")
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/servers/"+server.ID+"/deploy", map[string]any{})
	assertStatus(t, response, http.StatusAccepted)
	response.Body.Close()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start the server job")
	}
	response = requestJSON(t, app.client, http.MethodPut, app.server.URL+"/api/v1/servers/"+server.ID, map[string]any{
		"name": "busy", "host": "changed.example.com", "sshPort": 22, "sshUser": "root",
		"authMethod": "password", "httpPort": 3128, "socksPort": 1080,
		"dns": []string{"1.1.1.1"}, "tags": []string{},
	})
	assertStatus(t, response, http.StatusConflict)
	body := readBody(t, response)
	if !strings.Contains(body, "queued or running jobs") {
		t.Fatalf("busy server response is not actionable: %s", body)
	}
	close(executor.release)
}

func TestAPIMultiServerPartialFailureAndRetry(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	good := createTestServer(t, app, "good", "good.example.com", "good-ssh-password")
	bad := createTestServer(t, app, "bad", "fail.example.com", "bad-ssh-password")

	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users", map[string]any{
		"username": "fleet_user", "passwordMode": "generated", "serverIds": []string{good.ID, bad.ID},
	})
	assertStatus(t, response, http.StatusAccepted)
	body := readBody(t, response)
	var envelope struct {
		Data struct {
			User              ProxyUser `json:"user"`
			Job               Job       `json:"job"`
			GeneratedPassword string    `json:"generatedPassword"`
		} `json:"data"`
	}
	decodeBody(t, body, &envelope)
	if len(envelope.Data.GeneratedPassword) < 20 {
		t.Fatal("generated password was not returned once on creation")
	}
	job := waitForJob(t, app, envelope.Data.Job.ID)
	if job.Status != "partially_failed" || job.SuccessCount != 1 || job.FailedCount != 1 || job.Progress != 100 {
		t.Fatalf("unexpected partial job: %+v", job)
	}

	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/users/"+envelope.Data.User.ID, nil)
	assertStatus(t, response, http.StatusOK)
	body = readBody(t, response)
	var userEnvelope struct {
		Data ProxyUser `json:"data"`
	}
	decodeBody(t, body, &userEnvelope)
	if userEnvelope.Data.SyncStatus != "partial" {
		t.Fatalf("syncStatus = %s, want partial", userEnvelope.Data.SyncStatus)
	}
	if strings.Contains(body, envelope.Data.GeneratedPassword) {
		t.Fatal("generated password was returned after the creation response")
	}

	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/jobs/"+job.ID+"/retry", map[string]any{})
	assertStatus(t, response, http.StatusAccepted)
	retried := waitForJob(t, app, job.ID)
	if retried.Status != "partially_failed" || retried.SuccessCount != 1 || retried.FailedCount != 1 {
		t.Fatalf("unexpected retried job: %+v", retried)
	}
}

func TestAPIBatchDeploymentPersistsPerServerResults(t *testing.T) {
	app := newTestApplication(t, secretEchoExecutor{})
	loginTestClient(t, app)
	good := createTestServer(t, app, "batch-good", "batch-good.example.com", "good-password")
	bad := createTestServer(t, app, "batch-bad", "fail.batch.example.com", "bad-password")

	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/servers/actions/deploy", map[string]any{
		"serverIds": []string{good.ID, bad.ID, good.ID},
	})
	assertStatus(t, response, http.StatusAccepted)
	body := readBody(t, response)
	var envelope struct {
		Data struct {
			Job Job `json:"job"`
		} `json:"data"`
	}
	decodeBody(t, body, &envelope)
	job := waitForJob(t, app, envelope.Data.Job.ID)
	if job.Status != "partially_failed" || job.TargetCount != 2 || job.SuccessCount != 1 || job.FailedCount != 1 {
		t.Fatalf("unexpected batch deployment result: %+v", job)
	}
	if len(job.Targets) != 2 {
		t.Fatalf("job detail returned %d targets, want 2", len(job.Targets))
	}
	var failedTarget *JobTarget
	for index := range job.Targets {
		if job.Targets[index].ServerID == bad.ID {
			failedTarget = &job.Targets[index]
			break
		}
	}
	if failedTarget == nil || failedTarget.ServerName != bad.Name || failedTarget.Status != "failed" ||
		failedTarget.Attempt != 1 || failedTarget.Error == "" || failedTarget.StartedAt == nil || failedTarget.FinishedAt == nil {
		t.Fatalf("failed target is not diagnosable: %+v", failedTarget)
	}
	if strings.Contains(failedTarget.Error, "bad-password") || strings.Contains(failedTarget.Error, "credentialCipher") {
		t.Fatalf("job target leaked a secret: %q", failedTarget.Error)
	}
	goodServer, err := app.store.GetServer(context.Background(), good.ID)
	if err != nil {
		t.Fatal(err)
	}
	badServer, err := app.store.GetServer(context.Background(), bad.ID)
	if err != nil {
		t.Fatal(err)
	}
	if goodServer.InstallStatus != "installed" || badServer.InstallStatus != "failed" {
		t.Fatalf("unexpected per-server install state: good=%s bad=%s", goodServer.InstallStatus, badServer.InstallStatus)
	}

	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/servers/actions/service", map[string]any{
		"serverIds": []string{good.ID, bad.ID}, "action": "restart",
	})
	assertStatus(t, response, http.StatusAccepted)
	body = readBody(t, response)
	decodeBody(t, body, &envelope)
	job = waitForJob(t, app, envelope.Data.Job.ID)
	if job.Status != "partially_failed" || job.TargetCount != 2 {
		t.Fatalf("unexpected batch service result: %+v", job)
	}
	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/servers/actions/service", map[string]any{
		"serverIds": []string{good.ID, bad.ID}, "action": "reload",
	})
	assertStatus(t, response, http.StatusUnprocessableEntity)
	response.Body.Close()
}

func TestAPILoginRateLimit(t *testing.T) {
	app := newTestApplication(t, nil)
	for attempt := 0; attempt < 10; attempt++ {
		response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/auth/login", map[string]any{
			"username": "admin", "password": "wrong-password",
		})
		assertStatus(t, response, http.StatusUnauthorized)
		response.Body.Close()
	}
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/auth/login", map[string]any{
		"username": "admin", "password": developmentAdminPassword,
	})
	assertStatus(t, response, http.StatusTooManyRequests)
	if response.Header.Get("Retry-After") == "" {
		t.Fatal("rate-limited login response omitted Retry-After")
	}
	response.Body.Close()
}

func TestDeleteUserAfterLastServerWasRemoved(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	server := createTestServer(t, app, "temporary", "temporary.example.com", "ssh-secret")
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users", map[string]any{
		"username": "orphaned", "passwordMode": "generated", "serverIds": []string{server.ID},
	})
	assertStatus(t, response, http.StatusAccepted)
	body := readBody(t, response)
	var envelope struct {
		Data struct {
			User ProxyUser `json:"user"`
			Job  Job       `json:"job"`
		} `json:"data"`
	}
	decodeBody(t, body, &envelope)
	_ = waitForJob(t, app, envelope.Data.Job.ID)

	response = requestJSON(t, app.client, http.MethodDelete, app.server.URL+"/api/v1/servers/"+server.ID, nil)
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
	response = requestJSON(t, app.client, http.MethodDelete, app.server.URL+"/api/v1/users/"+envelope.Data.User.ID, nil)
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
	if _, err := app.store.GetProxyUser(context.Background(), envelope.Data.User.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbound user was not deleted: %v", err)
	}
}

func createTestServer(t *testing.T, app *testApplication, name, host, credential string) Server {
	t.Helper()
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/servers", map[string]any{
		"name": name, "host": host, "sshPort": 22, "sshUser": "root",
		"authMethod": "password", "credential": credential, "httpPort": 3128,
		"socksPort": 1080, "dns": []string{"1.1.1.1", "1.0.0.1"}, "tags": []string{},
	})
	assertStatus(t, response, http.StatusCreated)
	body := readBody(t, response)
	var envelope struct {
		Data Server `json:"data"`
	}
	decodeBody(t, body, &envelope)
	return envelope.Data
}

func loginTestClient(t *testing.T, app *testApplication) {
	t.Helper()
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/auth/login", map[string]any{
		"username": "admin", "password": developmentAdminPassword,
	})
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
}

func waitForJob(t *testing.T, app *testApplication, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/jobs/"+id, nil)
		assertStatus(t, response, http.StatusOK)
		body := readBody(t, response)
		var envelope struct {
			Data Job `json:"data"`
		}
		decodeBody(t, body, &envelope)
		if slicesTerminalJobStatus(envelope.Data.Status) {
			return envelope.Data
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not complete", id)
	return Job{}
}

func slicesTerminalJobStatus(status string) bool {
	return status == "succeeded" || status == "partially_failed" || status == "failed" || status == "cancelled"
}

func requestJSON(t *testing.T, client *http.Client, method, url string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	return response
}

func assertStatus(t *testing.T, response *http.Response, expected int) {
	t.Helper()
	if response.StatusCode != expected {
		body := readBody(t, response)
		t.Fatalf("status = %d, want %d; body=%s", response.StatusCode, expected, body)
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return string(body)
}

func decodeBody(t *testing.T, body string, destination any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), destination); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func TestInMemoryStoreUsesOneSQLiteConnection(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if maximum := store.db.Stats().MaxOpenConnections; maximum != 1 {
		t.Fatalf("in-memory SQLite max connections = %d, want 1", maximum)
	}
	now := time.Now().UTC()
	serverID := newID()
	if err := store.CreateServer(context.Background(), Server{
		ID: serverID, Name: "memory", Host: "memory.example.com", SSHPort: 22, SSHUser: "root",
		AuthMethod: "password", CredentialCipher: "cipher", Status: "unknown",
		ServiceStatus: "unknown", InstallStatus: "not_installed", PublicIP: "memory.example.com",
		HTTPPort: 3128, SocksPort: 1080, DNS: []string{"1.1.1.1"}, Tags: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	userID := newID()
	if err := store.CreateProxyUser(context.Background(), ProxyUser{
		ID: userID, Username: "memory_user", PasswordCipher: "cipher", ServerIDs: []string{serverID}, CreatedAt: now, UpdatedAt: now,
	}, NewJob{
		ID: newID(), Type: "user_create", Actor: "test", EntityType: "proxy_user", EntityID: userID,
		Targets: []NewJobTarget{{ID: newID(), ServerID: serverID, Payload: `{}`}},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	users, err := store.ListProxyUsers(ctx, "", "", "")
	if err != nil {
		t.Fatalf("query populated in-memory database: %v", err)
	}
	if len(users) != 1 || len(users[0].ServerIDs) != 1 || users[0].ServerIDs[0] != serverID {
		t.Fatalf("unexpected in-memory proxy users: %+v", users)
	}
}

func TestWorkerPersistsPerTargetPartialResult(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	key := []byte("0123456789abcdef0123456789abcdef")
	box, err := NewSecretBox(key)
	if err != nil {
		t.Fatalf("NewSecretBox: %v", err)
	}
	now := time.Now().UTC()
	serverIDs := []string{newID(), newID()}
	hosts := []string{"worker-good.example.com", "fail.worker.example.com"}
	for index, id := range serverIDs {
		ciphertext, err := box.Encrypt([]byte("worker-ssh-secret"), "server:"+id+":credential")
		if err != nil {
			t.Fatal(err)
		}
		server := Server{
			ID: id, Name: "worker-server-" + hosts[index], Host: hosts[index], SSHPort: 22,
			SSHUser: "root", AuthMethod: "password", CredentialCipher: ciphertext,
			Status: "unknown", ServiceStatus: "unknown", InstallStatus: "not_installed",
			PublicIP: hosts[index], HTTPPort: 3128, SocksPort: 1080,
			DNS: []string{"1.1.1.1", "1.0.0.1"}, Tags: []string{}, CreatedAt: now, UpdatedAt: now,
		}
		if err := store.CreateServer(context.Background(), server); err != nil {
			t.Fatalf("CreateServer: %v", err)
		}
	}
	taskJSON, _ := json.Marshal(TargetTask{Action: "inspect"})
	jobID := newID()
	job := NewJob{
		ID: jobID, Type: "connection_test", Actor: "test",
		Targets: []NewJobTarget{
			{ID: newID(), ServerID: serverIDs[0], Payload: string(taskJSON)},
			{ID: newID(), ServerID: serverIDs[1], Payload: string(taskJSON)},
		},
	}
	if err := store.CreateJob(context.Background(), job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	cfg := Config{WorkerConcurrency: 1, WorkerPoll: time.Millisecond}
	worker := NewWorker(store, box, &MockExecutor{Delay: time.Millisecond}, cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	for index := 0; index < 2; index++ {
		processed, err := worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatalf("ProcessOne %d: %v", index, err)
		}
		if !processed {
			t.Fatalf("ProcessOne %d did not claim a target", index)
		}
	}
	result, err := store.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if result.Status != "partially_failed" || result.SuccessCount != 1 || result.FailedCount != 1 {
		t.Fatalf("unexpected job result: %+v", result)
	}

	goodServer, _ := store.GetServer(context.Background(), serverIDs[0])
	badServer, _ := store.GetServer(context.Background(), serverIDs[1])
	if goodServer.Status != "online" || badServer.Status != "offline" {
		t.Fatalf("unexpected reported server states: good=%s bad=%s", goodServer.Status, badServer.Status)
	}
}

func TestRecoverInterruptedJobs(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "recover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	box, _ := NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	id := newID()
	ciphertext, _ := box.Encrypt([]byte("secret"), "server:"+id+":credential")
	now := time.Now().UTC()
	if err := store.CreateServer(context.Background(), Server{
		ID: id, Name: "recover", Host: "recover.example.com", SSHPort: 22, SSHUser: "root",
		AuthMethod: "password", CredentialCipher: ciphertext, Status: "unknown",
		ServiceStatus: "unknown", InstallStatus: "not_installed", PublicIP: "recover.example.com",
		HTTPPort: 3128, SocksPort: 1080, DNS: []string{"1.1.1.1"}, Tags: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(TargetTask{Action: "inspect"})
	jobID := newID()
	if err := store.CreateJob(context.Background(), NewJob{
		ID: jobID, Type: "connection_test", Actor: "test",
		Targets: []NewJobTarget{{ID: newID(), ServerID: id, Payload: string(payload)}},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTarget(context.Background())
	if err != nil || claimed == nil {
		t.Fatalf("ClaimTarget: claimed=%v err=%v", claimed, err)
	}
	if err := store.RecoverInterruptedJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	claimedAgain, err := store.ClaimTarget(context.Background())
	if err != nil || claimedAgain == nil || claimedAgain.Target.ID != claimed.Target.ID {
		t.Fatalf("interrupted target was not re-queued: claimed=%v err=%v", claimedAgain, err)
	}
}

func TestRetryRejectsStaleTargetIntent(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "stale-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	box, _ := NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	serverID := newID()
	ciphertext, _ := box.Encrypt([]byte("secret"), "server:"+serverID+":credential")
	now := time.Now().UTC()
	if err := store.CreateServer(context.Background(), Server{
		ID: serverID, Name: "stale", Host: "stale.example.com", SSHPort: 22, SSHUser: "root",
		AuthMethod: "password", CredentialCipher: ciphertext, Status: "unknown",
		ServiceStatus: "unknown", InstallStatus: "not_installed", PublicIP: "stale.example.com",
		HTTPPort: 3128, SocksPort: 1080, DNS: []string{"1.1.1.1"}, Tags: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(TargetTask{Action: "service", ServiceAction: "stop"})
	oldJobID := newID()
	if err := store.CreateJob(context.Background(), NewJob{
		ID: oldJobID, Type: "service_stop", Actor: "test", EntityType: "fleet",
		Targets: []NewJobTarget{{ID: newID(), ServerID: serverID, Payload: string(payload)}},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTarget(context.Background())
	if err != nil || claimed == nil {
		t.Fatalf("claim old target: claimed=%v err=%v", claimed, err)
	}
	if err := store.CompleteTarget(context.Background(), *claimed, false, "failed", ExecutionUpdate{}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJob(context.Background(), NewJob{
		ID: newID(), Type: "service_start", Actor: "test", EntityType: "fleet",
		Targets: []NewJobTarget{{ID: newID(), ServerID: serverID, Payload: string(payload)}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryJob(context.Background(), oldJobID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale retry error = %v, want ErrConflict", err)
	}
}

func TestRecoverStaleTargetsStopsAfterThreeAttempts(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "stale-target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	box, _ := NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	serverID := newID()
	ciphertext, _ := box.Encrypt([]byte("secret"), "server:"+serverID+":credential")
	now := time.Now().UTC()
	if err := store.CreateServer(context.Background(), Server{
		ID: serverID, Name: "stale-target", Host: "stale-target.example.com", SSHPort: 22, SSHUser: "root",
		AuthMethod: "password", CredentialCipher: ciphertext, Status: "unknown",
		ServiceStatus: "unknown", InstallStatus: "not_installed", PublicIP: "stale-target.example.com",
		HTTPPort: 3128, SocksPort: 1080, DNS: []string{"1.1.1.1"}, Tags: []string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(TargetTask{Action: "inspect"})
	jobID := newID()
	if err := store.CreateJob(context.Background(), NewJob{
		ID: jobID, Type: "connection_test", Actor: "test",
		Targets: []NewJobTarget{{ID: newID(), ServerID: serverID, Payload: string(payload)}},
	}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		claimed, err := store.ClaimTarget(context.Background())
		if err != nil || claimed == nil {
			t.Fatalf("claim attempt %d: claimed=%v err=%v", attempt, claimed, err)
		}
		if _, err := store.db.Exec("UPDATE job_targets SET started_at = ? WHERE id = ?", formatTime(now.Add(-time.Hour)), claimed.Target.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.RecoverStaleTargets(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	job, err := store.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.FailedCount != 1 {
		t.Fatalf("exhausted stale job did not fail: %+v", job)
	}
	if claimed, err := store.ClaimTarget(context.Background()); err != nil || claimed != nil {
		t.Fatalf("exhausted target was claimable: claimed=%v err=%v", claimed, err)
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// policyFailureBackoff delays another automatic policy synchronization of a
// server after one failed.
const policyFailureBackoff = 15 * time.Minute

// TrafficManager runs the two background loops of the usage limits:
//
//   - collection reads the traffic counters of every installed server directly
//     through the executor (not the job queue, so it does not flood the jobs
//     list) and stores them;
//   - reconciliation starts due periodic resets and queues a policy
//     synchronization for every server whose node state differs from the
//     desired one (expired, exhausted, reset, cap drift, …).
type TrafficManager struct {
	store     *Store
	box       *SecretBox
	executor  Executor
	worker    *Worker
	logger    *slog.Logger
	collect   time.Duration
	reconcile time.Duration
	parallel  int
	waitGroup sync.WaitGroup
}

func NewTrafficManager(store *Store, box *SecretBox, executor Executor, worker *Worker, cfg Config, logger *slog.Logger) *TrafficManager {
	parallel := cfg.WorkerConcurrency
	if parallel < 1 {
		parallel = 1
	}
	return &TrafficManager{
		store: store, box: box, executor: executor, worker: worker, logger: logger,
		collect: cfg.TrafficSyncInterval, reconcile: cfg.TrafficReconcileInterval, parallel: parallel,
	}
}

func (m *TrafficManager) Start(ctx context.Context) {
	if m.collect > 0 {
		m.waitGroup.Add(1)
		go m.loop(ctx, m.collect, func() {
			m.CollectOnce(ctx)
			m.runReconcile(ctx)
		})
	}
	if m.reconcile > 0 {
		m.waitGroup.Add(1)
		go m.loop(ctx, m.reconcile, func() { m.runReconcile(ctx) })
	}
}

func (m *TrafficManager) Wait() {
	m.waitGroup.Wait()
}

func (m *TrafficManager) loop(ctx context.Context, interval time.Duration, run func()) {
	defer m.waitGroup.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (m *TrafficManager) runReconcile(ctx context.Context) {
	if _, err := m.ReconcileOnce(ctx, time.Now()); err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Error("traffic policy reconciliation failed", "error", sanitizeMessage(err.Error()))
	}
}

// CollectOnce reads the traffic counters of every installed server.
func (m *TrafficManager) CollectOnce(ctx context.Context) {
	servers, err := m.store.ListServers(ctx, "", "installed")
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			m.logger.Error("list servers for traffic collection failed", "error", sanitizeMessage(err.Error()))
		}
		return
	}
	semaphore := make(chan struct{}, m.parallel)
	var group sync.WaitGroup
	pinning, pinsHostKeys := m.executor.(hostKeyPinning)
	for _, server := range servers {
		if pinsHostKeys && pinning.PinsHostKeys() && server.HostFingerprint == "" {
			// Host keys are only enrolled by operator-initiated jobs; never
			// send credentials to an unverified host in the background.
			continue
		}
		group.Add(1)
		semaphore <- struct{}{}
		go func(server Server) {
			defer group.Done()
			defer func() { <-semaphore }()
			m.collectServer(ctx, server)
		}(server)
	}
	group.Wait()
}

func (m *TrafficManager) collectServer(ctx context.Context, server Server) {
	credential, err := m.worker.serverCredential(server)
	if err != nil {
		m.recordSync(ctx, server, nil, errors.New("stored SSH credential cannot be decrypted"))
		return
	}
	result, err := m.executor.Execute(ctx, ExecutionRequest{
		Server: server, Credential: credential, Task: TargetTask{Action: "traffic"},
	})
	credential = ""
	if ctx.Err() != nil {
		return
	}
	if err == nil && result.Update.Traffic == nil {
		err = errors.New("node did not return a traffic report")
	}
	m.recordSync(ctx, server, result.Update.Traffic, err)
}

func (m *TrafficManager) recordSync(ctx context.Context, server Server, report *TrafficReport, syncErr error) {
	if err := m.store.RecordTrafficSync(ctx, server.ID, report, syncErr); err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Error("store traffic report failed", "server", server.ID, "error", sanitizeMessage(err.Error()))
	}
}

// ReconcileOnce starts due resets, prunes obsolete accounting rows and queues
// a policy synchronization for each server that needs one. It returns the IDs
// of the queued jobs.
func (m *TrafficManager) ReconcileOnce(ctx context.Context, now time.Time) ([]string, error) {
	if _, err := m.store.ProcessDueResets(ctx, now); err != nil {
		return nil, err
	}
	if err := m.store.PruneTraffic(ctx); err != nil {
		return nil, err
	}
	inputs, err := m.store.ServerPolicyInputs(ctx, "", now)
	if err != nil {
		return nil, err
	}
	pending := make(map[string]struct{})
	for _, input := range inputs {
		if _, ok := pending[input.ServerID]; ok {
			continue
		}
		desired := desiredNodePolicy(input.User, input.Observed, now)
		if policyNeedsPush(input.User, desired, input.Observed) {
			pending[input.ServerID] = struct{}{}
		}
	}
	serverIDs := make([]string, 0, len(pending))
	for serverID := range pending {
		serverIDs = append(serverIDs, serverID)
	}
	sort.Strings(serverIDs)
	queued := make([]string, 0)
	for _, serverID := range serverIDs {
		server, err := m.store.GetServer(ctx, serverID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return queued, err
		}
		if server.InstallStatus != "installed" {
			continue
		}
		jobID, err := m.store.EnqueuePolicyJob(ctx, server, now, policyFailureBackoff)
		if errors.Is(err, ErrStale) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return queued, fmt.Errorf("queue policy synchronization: %w", err)
		}
		if jobID != "" {
			queued = append(queued, jobID)
		}
	}
	if len(queued) > 0 {
		m.worker.Notify()
	}
	return queued, nil
}

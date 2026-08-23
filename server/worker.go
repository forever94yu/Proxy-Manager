package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Worker struct {
	store       *Store
	box         *SecretBox
	executor    Executor
	concurrency int
	poll        time.Duration
	logger      *slog.Logger
	wake        chan struct{}
	waitGroup   sync.WaitGroup
	staleAfter  time.Duration
}

func NewWorker(store *Store, box *SecretBox, executor Executor, cfg Config, logger *slog.Logger) *Worker {
	return &Worker{
		store:       store,
		box:         box,
		executor:    executor,
		concurrency: cfg.WorkerConcurrency,
		poll:        cfg.WorkerPoll,
		logger:      logger,
		wake:        make(chan struct{}, 1),
		staleAfter:  staleTargetDuration(cfg),
	}
}

func (w *Worker) Start(ctx context.Context) error {
	if err := w.store.RecoverInterruptedJobs(ctx); err != nil {
		return fmt.Errorf("recover interrupted jobs: %w", err)
	}
	for index := 0; index < w.concurrency; index++ {
		w.waitGroup.Add(1)
		go w.loop(ctx, index)
	}
	w.waitGroup.Add(1)
	go w.recoveryLoop(ctx)
	return nil
}

func (w *Worker) Wait() {
	w.waitGroup.Wait()
}

func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) loop(ctx context.Context, index int) {
	defer w.waitGroup.Done()
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		processed, err := w.ProcessOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("job worker iteration failed", "worker", index, "error", sanitizeMessage(err.Error()))
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	claimed, err := w.store.ClaimTarget(ctx)
	if err != nil {
		return false, err
	}
	if claimed == nil {
		return false, nil
	}
	server, err := w.store.GetServer(ctx, claimed.Target.ServerID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			message := "Server inventory could not be read"
			if completeErr := w.completeTarget(ctx, *claimed, false, message, ExecutionUpdate{}); completeErr != nil {
				return true, fmt.Errorf("get target server: %v; complete target: %w", err, completeErr)
			}
			return true, fmt.Errorf("get target server: %w", err)
		}
		message := "Target server no longer exists"
		if completeErr := w.completeTarget(ctx, *claimed, false, message, ExecutionUpdate{}); completeErr != nil {
			return true, fmt.Errorf("complete missing-server target: %w", completeErr)
		}
		return true, nil
	}
	credentialBytes, err := w.box.Decrypt(server.CredentialCipher, "server:"+server.ID+":credential")
	if err != nil {
		message := "Stored SSH credential cannot be decrypted"
		if completeErr := w.completeTarget(ctx, *claimed, false, message, ExecutionUpdate{}); completeErr != nil {
			return true, completeErr
		}
		return true, nil
	}
	credential := string(credentialBytes)
	for index := range credentialBytes {
		credentialBytes[index] = 0
	}
	var task TargetTask
	decoderErr := json.Unmarshal([]byte(claimed.Target.Payload), &task)
	if decoderErr != nil {
		message := "Stored job payload is invalid"
		if completeErr := w.completeTarget(ctx, *claimed, false, message, ExecutionUpdate{}); completeErr != nil {
			return true, completeErr
		}
		return true, nil
	}
	password := ""
	if task.PasswordCiphertext != "" {
		passwordBytes, err := w.box.Decrypt(task.PasswordCiphertext, "job:"+claimed.Job.ID+":password")
		if err != nil {
			message := "Stored proxy password cannot be decrypted"
			if completeErr := w.completeTarget(ctx, *claimed, false, message, ExecutionUpdate{}); completeErr != nil {
				return true, completeErr
			}
			return true, nil
		}
		password = string(passwordBytes)
		for index := range passwordBytes {
			passwordBytes[index] = 0
		}
	}

	result, executeErr := w.executor.Execute(ctx, ExecutionRequest{
		Server: server, Credential: credential, Task: task, Password: password,
	})
	if executeErr != nil {
		switch task.Action {
		case "deploy":
			result.Update.InstallStatus = "failed"
		case "service":
			result.Update.ServiceStatus = "failed"
		}
	}
	message := redactSecrets(result.Message, password, credential)
	if executeErr != nil {
		message = redactSecrets(executeErr.Error(), password, credential)
	}
	credential = ""
	password = ""
	message = sanitizeMessage(message)
	if err := w.completeTarget(ctx, *claimed, executeErr == nil, message, result.Update); err != nil {
		return true, fmt.Errorf("complete job target: %w", err)
	}
	return true, nil
}

func (w *Worker) completeTarget(ctx context.Context, claimed ClaimedTarget, success bool, message string, update ExecutionUpdate) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := w.store.CompleteTarget(ctx, claimed, success, message, update); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			return lastErr
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return lastErr
		case <-timer.C:
		}
	}
	return lastErr
}

func (w *Worker) recoveryLoop(ctx context.Context) {
	defer w.waitGroup.Done()
	interval := w.staleAfter / 4
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := w.store.RecoverStaleTargets(ctx, now.Add(-w.staleAfter)); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.Error("stale job recovery failed", "error", sanitizeMessage(err.Error()))
			}
		}
	}
}

func staleTargetDuration(cfg Config) time.Duration {
	duration := 2*cfg.CommandTimeout + cfg.SSHTimeout + 5*time.Minute
	if duration < 10*time.Minute {
		return 10 * time.Minute
	}
	return duration
}

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := LoadConfig()
	if err != nil {
		logger.Error("configuration is invalid", "error", err)
		os.Exit(1)
	}
	store, err := OpenStore(cfg.DatabasePath)
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	box, err := NewSecretBox(cfg.MasterKey)
	if err != nil {
		logger.Error("encryption initialization failed", "error", err)
		os.Exit(1)
	}

	var executor Executor
	if cfg.ExecutorMode == "ssh" {
		executor = &SSHExecutor{
			ScriptPath: cfg.ScriptPath, ConnectTimeout: cfg.SSHTimeout, CommandTimeout: cfg.CommandTimeout,
		}
	} else {
		executor = &MockExecutor{}
	}
	sessions := NewSessionManager(cfg)
	worker := NewWorker(store, box, executor, cfg, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := worker.Start(ctx); err != nil {
		logger.Error("worker initialization failed", "error", err)
		os.Exit(1)
	}
	api := NewAPI(cfg, store, box, sessions, worker, logger)
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("proxy manager API listening", "address", cfg.HTTPAddr, "environment", cfg.Environment, "executor", cfg.ExecutorMode)
		serverErrors <- httpServer.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			stop()
		}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		logger.Error("HTTP shutdown failed", "error", err)
	}
	worker.Wait()
}

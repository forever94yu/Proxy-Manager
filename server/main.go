package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		printVersion()
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// A release installed by an online upgrade replaces this program: run it
	// as a child process and restart it whenever it installs another one.
	updateDir, _ := updateDirFromEnv()
	if updateDir != "" && !launched() {
		if code, ran := runInstalledRelease(updateDir, logger); ran {
			os.Exit(code)
		}
	}
	for {
		code, restart := serve(logger)
		if !restart {
			os.Exit(code)
		}
		if launched() {
			os.Exit(exitRestart)
		}
		if code, ran := runInstalledRelease(updateDir, logger); ran {
			os.Exit(code)
		}
		// The new release failed to start and was rolled back to this
		// program, so serve again.
	}
}

// serve runs the control plane until it is stopped. restart is set when an
// online upgrade was installed and the new release should be started.
func serve(logger *slog.Logger) (code int, restart bool) {
	cfg, err := LoadConfig()
	if err != nil {
		logger.Error("configuration is invalid", "error", err)
		return 1, false
	}
	store, err := OpenStore(cfg.DatabasePath)
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		return 1, false
	}
	defer store.Close()
	box, err := NewSecretBox(cfg.MasterKey)
	if err != nil {
		logger.Error("encryption initialization failed", "error", err)
		return 1, false
	}

	var executor Executor
	if cfg.ExecutorMode == "ssh" {
		executor = &SSHExecutor{
			ScriptPath: cfg.ScriptPath, ConnectTimeout: cfg.SSHTimeout, CommandTimeout: cfg.CommandTimeout,
		}
	} else {
		// Simulated usage lets the quota flow be exercised without nodes.
		executor = &MockExecutor{SimulatedTrafficBytes: 32 << 20}
	}
	sessions := NewSessionManager(cfg)
	worker := NewWorker(store, box, executor, cfg, logger)
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Cancelled on a signal, a server failure or an update restart. The signal
	// handler stays registered until serve returns, so a repeated signal does
	// not kill the process during the graceful shutdown.
	ctx, cancel := context.WithCancel(signalContext)
	defer cancel()
	if err := worker.Start(ctx); err != nil {
		logger.Error("worker initialization failed", "error", err)
		return 1, false
	}
	traffic := NewTrafficManager(store, box, executor, worker, cfg, logger)
	traffic.Start(ctx)
	updater := NewUpdater(cfg, store, logger)
	api := NewAPI(cfg, store, box, sessions, worker, updater, logger)
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		logger.Error("HTTP listen failed", "address", cfg.HTTPAddr, "error", err)
		cancel()
		worker.Wait()
		traffic.Wait()
		return 1, false
	}
	// Database migrations ran and the port is bound: a freshly installed
	// release is no longer rolled back if it exits.
	if err := markReleaseHealthy(cfg.UpdateDir); err != nil {
		logger.Warn("release state could not be updated", "error", err)
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("proxy manager API listening", "address", cfg.HTTPAddr, "version", Version,
			"environment", cfg.Environment, "executor", cfg.ExecutorMode)
		serverErrors <- httpServer.Serve(listener)
	}()
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			code = 1
		}
	case <-updater.RestartRequested():
		restart = true
	}
	cancel()

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		logger.Error("HTTP shutdown failed", "error", err)
	}
	worker.Wait()
	traffic.Wait()
	return code, restart
}

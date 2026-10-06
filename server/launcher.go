package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	// releaseDirEnv is set for a release started by the launcher; it points at
	// the installed release directory.
	releaseDirEnv = "PM_RELEASE_DIR"
	// launcherEnv marks a process that was started by the launcher, so it asks
	// the launcher to restart it instead of restarting itself.
	launcherEnv = "PM_LAUNCHED"
	// exitRestart is the exit code a launched release uses to ask the launcher
	// to start the release recorded in the state file.
	exitRestart = 75
	// childStopTimeout bounds how long the launcher waits for a release to
	// shut down after forwarding a signal.
	childStopTimeout = 30 * time.Second
)

// releaseState is stored as state.json in the releases directory.
type releaseState struct {
	Current  string `json:"current"`
	Previous string `json:"previous,omitempty"`
	// Pending is set when Current was just installed and has not started
	// successfully yet; a release that exits while pending is rolled back.
	Pending       bool      `json:"pending,omitempty"`
	FailedVersion string    `json:"failedVersion,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func releaseBinaryName() string {
	if runtime.GOOS == "windows" {
		return "proxy-manager.exe"
	}
	return "proxy-manager"
}

func releaseDir(root, version string) string {
	return filepath.Join(root, "v"+version)
}

func readReleaseState(root string) (releaseState, error) {
	var state releaseState
	content, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return state, err
	}
	return state, nil
}

func writeReleaseState(root string, state releaseState) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(root, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(append(content, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), filepath.Join(root, "state.json"))
}

// markReleaseHealthy clears the pending flag once a freshly installed release
// has opened the database and is listening.
func markReleaseHealthy(root string) error {
	if root == "" || os.Getenv(releaseDirEnv) == "" {
		return nil
	}
	state, err := readReleaseState(root)
	if err != nil || !state.Pending || state.Current != Version {
		return err
	}
	state.Pending = false
	state.UpdatedAt = time.Now().UTC()
	return writeReleaseState(root, state)
}

// installedRelease returns the directory of the installed release to run
// instead of this program, or "" when this program is at least as new.
func installedRelease(root string) (releaseState, string) {
	state, err := readReleaseState(root)
	if err != nil || normalizeVersion(state.Current) == "" || compareVersions(state.Current, Version) <= 0 {
		return state, ""
	}
	dir := releaseDir(root, state.Current)
	if info, err := os.Stat(filepath.Join(dir, releaseBinaryName())); err != nil || !info.Mode().IsRegular() {
		return state, ""
	}
	return state, dir
}

// runInstalledRelease runs the newest installed release as a child process
// until it exits for good. ran is false when no newer release is installed, or
// a failed one was rolled back to a release that is not newer, and this
// program should serve itself.
func runInstalledRelease(root string, logger *slog.Logger) (code int, ran bool) {
	for {
		state, dir := installedRelease(root)
		if dir == "" {
			return 0, false
		}
		logger.Info("starting installed release", "version", state.Current, "directory", dir)
		exitCode, signalled, err := runRelease(dir, logger)
		if err != nil {
			logger.Error("installed release could not be started", "version", state.Current, "error", err)
		}
		switch {
		case signalled:
			return exitCode, true
		case err == nil && exitCode == exitRestart:
			continue
		}
		current, readErr := readReleaseState(root)
		if readErr == nil && current.Pending && current.Current == state.Current {
			logger.Error("new release failed to start; rolling back", "version", current.Current, "previous", current.Previous, "exitCode", exitCode)
			rolledBack := releaseState{Current: current.Previous, FailedVersion: current.Current, UpdatedAt: time.Now().UTC()}
			if writeErr := writeReleaseState(root, rolledBack); writeErr != nil {
				logger.Error("release rollback failed", "error", writeErr)
				return 1, true
			}
			continue
		}
		if err != nil {
			return 1, true
		}
		return exitCode, true
	}
}

// runRelease starts the release in dir and forwards termination signals to it.
// signalled reports whether the launcher itself was asked to stop.
func runRelease(dir string, logger *slog.Logger) (exitCode int, signalled bool, err error) {
	command := exec.Command(filepath.Join(dir, releaseBinaryName()), os.Args[1:]...)
	command.Env = append(withoutEnv(os.Environ(), releaseDirEnv, launcherEnv), releaseDirEnv+"="+dir, launcherEnv+"=1")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := command.Start(); err != nil {
		return 1, false, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	for {
		select {
		case received := <-signals:
			signalled = true
			// On Windows a console interrupt already reaches the child and
			// Signal is unsupported, so the timeout below is the fallback.
			if signalErr := command.Process.Signal(received); signalErr != nil && runtime.GOOS != "windows" {
				logger.Warn("forwarding signal to release failed", "error", signalErr)
			}
			select {
			case waitErr := <-done:
				return processExitCode(command, waitErr), true, nil
			case <-time.After(childStopTimeout):
				command.Process.Kill()
				<-done
				return 1, true, nil
			}
		case waitErr := <-done:
			var exitErr *exec.ExitError
			if waitErr != nil && !errors.As(waitErr, &exitErr) {
				return 1, signalled, waitErr
			}
			return processExitCode(command, waitErr), signalled, nil
		}
	}
}

func processExitCode(command *exec.Cmd, waitErr error) int {
	if command.ProcessState == nil {
		return 1
	}
	if code := command.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	if waitErr != nil {
		return 1
	}
	return 0
}

func withoutEnv(environment []string, names ...string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		keep := true
		for _, excluded := range names {
			if strings.EqualFold(name, excluded) {
				keep = false
				break
			}
		}
		if keep {
			result = append(result, entry)
		}
	}
	return result
}

// launched reports whether this process is a release started by a launcher.
func launched() bool {
	return os.Getenv(launcherEnv) != ""
}

func printVersion() {
	fmt.Println(Version)
}

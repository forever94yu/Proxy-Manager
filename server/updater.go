package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultUpdateAPIURL = "https://api.github.com"
	// updateCheckTTL is how long a release check is reused; GitHub allows 60
	// unauthenticated API requests per hour and address.
	updateCheckTTL      = time.Hour
	updateRefreshMinGap = 10 * time.Second
	maxReleaseJSONBytes = 4 << 20
	maxChecksumBytes    = 64 << 10
	maxPackageBytes     = 200 << 20
	maxExtractedBytes   = 400 << 20
	maxReleaseNotes     = 20000
	databaseBackupsKept = 3
	restartJobGrace     = 2 * time.Minute
)

const (
	updatePhaseIdle        = "idle"
	updatePhaseDownloading = "downloading"
	updatePhaseInstalling  = "installing"
	updatePhaseRestarting  = "restarting"
	updatePhaseFailed      = "failed"
)

var (
	ErrUpdateDisabled   = errors.New("Online updates are disabled")
	ErrUpdateInProgress = errors.New("An update is already in progress")
	ErrUpdateNotLatest  = errors.New("The requested version is not the latest release; check for updates again")
	ErrUpdateCurrent    = errors.New("Proxy Manager is already running the latest version")
	ErrUpdateNoPackage  = errors.New("The release has no package for this platform")
	ErrUpdateJobsActive = errors.New("Jobs are running; wait for them to finish before updating")
)

// ReleaseInfo describes the latest published release of the update repository.
type ReleaseInfo struct {
	Version     string    `json:"version"`
	Name        string    `json:"name"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
	// PackageName is the archive for this platform; empty when the release
	// does not ship one.
	PackageName string `json:"packageName,omitempty"`
	PackageSize int64  `json:"packageSize,omitempty"`

	pkg       *releaseAsset
	checksums *releaseAsset
}

type releaseAsset struct {
	Name   string
	URL    string
	Size   int64
	Digest string
}

// UpdateStatus is the payload of GET /api/v1/system/update.
type UpdateStatus struct {
	CurrentVersion  string       `json:"currentVersion"`
	Platform        string       `json:"platform"`
	Enabled         bool         `json:"enabled"`
	Repository      string       `json:"repository"`
	Latest          *ReleaseInfo `json:"latest,omitempty"`
	UpdateAvailable bool         `json:"updateAvailable"`
	CheckedAt       *time.Time   `json:"checkedAt,omitempty"`
	CheckError      string       `json:"checkError,omitempty"`
	Phase           string       `json:"phase"`
	TargetVersion   string       `json:"targetVersion,omitempty"`
	DownloadedBytes int64        `json:"downloadedBytes,omitempty"`
	TotalBytes      int64        `json:"totalBytes,omitempty"`
	Error           string       `json:"error,omitempty"`
	// FailedVersion is a release that did not start after an upgrade and was
	// rolled back automatically.
	FailedVersion string `json:"failedVersion,omitempty"`
}

// Updater checks the GitHub releases of the update repository and installs a
// newer release into the releases directory. The launcher (launcher.go) starts
// the installed release on the next process start; RestartRequested tells main
// to shut down gracefully and restart into it.
type Updater struct {
	enabled    bool
	repository string
	dir        string
	apiURL     string
	store      *Store
	logger     *slog.Logger
	client     *http.Client
	restart    chan struct{}

	checkMu sync.Mutex

	mu         sync.Mutex
	latest     *ReleaseInfo
	checkedAt  time.Time
	checkError string
	phase      string
	target     string
	failure    string
	downloaded atomic.Int64
	total      atomic.Int64
}

func NewUpdater(cfg Config, store *Store, logger *slog.Logger) *Updater {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Updater{
		enabled:    cfg.UpdateDir != "",
		repository: cfg.UpdateRepository,
		dir:        cfg.UpdateDir,
		apiURL:     strings.TrimRight(envOrDefault("UPDATE_API_URL", defaultUpdateAPIURL), "/"),
		store:      store,
		logger:     logger,
		client:     &http.Client{Transport: transport},
		restart:    make(chan struct{}, 1),
		phase:      updatePhaseIdle,
	}
}

// RestartRequested is signalled once an update is installed.
func (u *Updater) RestartRequested() <-chan struct{} {
	return u.restart
}

// Status returns the update state, checking GitHub when the cached result is
// older than updateCheckTTL or refresh is set.
func (u *Updater) Status(ctx context.Context, refresh bool) UpdateStatus {
	if u.enabled {
		u.mu.Lock()
		age := time.Since(u.checkedAt)
		due := u.checkedAt.IsZero() || age > updateCheckTTL || (refresh && age > updateRefreshMinGap)
		u.mu.Unlock()
		if due {
			u.check(ctx)
		}
	}
	return u.snapshot()
}

func (u *Updater) snapshot() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	status := UpdateStatus{
		CurrentVersion: Version,
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		Enabled:        u.enabled,
		Repository:     u.repository,
		CheckError:     u.checkError,
		Phase:          u.phase,
		TargetVersion:  u.target,
		Error:          u.failure,
	}
	if u.latest != nil {
		latest := *u.latest
		status.Latest = &latest
		status.UpdateAvailable = compareVersions(latest.Version, Version) > 0
	}
	if !u.checkedAt.IsZero() {
		checkedAt := u.checkedAt.UTC()
		status.CheckedAt = &checkedAt
	}
	if u.phase == updatePhaseDownloading || u.phase == updatePhaseInstalling {
		status.DownloadedBytes = u.downloaded.Load()
		status.TotalBytes = u.total.Load()
	}
	if u.dir != "" {
		if state, err := readReleaseState(u.dir); err == nil {
			status.FailedVersion = state.FailedVersion
		}
	}
	return status
}

func (u *Updater) check(ctx context.Context) {
	u.checkMu.Lock()
	defer u.checkMu.Unlock()
	u.mu.Lock()
	fresh := !u.checkedAt.IsZero() && time.Since(u.checkedAt) < updateRefreshMinGap
	u.mu.Unlock()
	if fresh {
		// A concurrent request has just checked.
		return
	}
	// The result is cached and shared, so a client that disconnects must not
	// cancel the check.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	release, err := u.fetchLatest(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checkedAt = time.Now()
	if err != nil {
		u.checkError = err.Error()
		u.logger.Warn("update check failed", "repository", u.repository, "error", sanitizeMessage(err.Error()))
		return
	}
	u.latest = release
	u.checkError = ""
}

func (u *Updater) fetchLatest(ctx context.Context) (*ReleaseInfo, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.apiURL+"/repos/"+u.repository+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "Proxy-Manager/"+Version)
	response, err := u.client.Do(request)
	if err != nil {
		return nil, errors.New("GitHub could not be reached")
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, errors.New("No published release was found")
	case (response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests) &&
		response.Header.Get("X-RateLimit-Remaining") == "0":
		return nil, errors.New("GitHub API rate limit exceeded; try again later")
	case response.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered HTTP %d", response.StatusCode)
	}
	var payload struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		Assets      []struct {
			Name               string `json:"name"`
			Size               int64  `json:"size"`
			Digest             string `json:"digest"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxReleaseJSONBytes)).Decode(&payload); err != nil {
		return nil, errors.New("Release information is invalid")
	}
	version := normalizeVersion(payload.TagName)
	if version == "" || payload.Draft || payload.Prerelease {
		return nil, errors.New("Release information is invalid")
	}
	notes := payload.Body
	if len([]rune(notes)) > maxReleaseNotes {
		notes = string([]rune(notes)[:maxReleaseNotes]) + "\n…"
	}
	release := &ReleaseInfo{
		Version: version, Name: payload.Name, Notes: notes, URL: payload.HTMLURL, PublishedAt: payload.PublishedAt,
	}
	packageName := releasePackageName(version)
	for _, asset := range payload.Assets {
		entry := &releaseAsset{Name: asset.Name, URL: asset.BrowserDownloadURL, Size: asset.Size, Digest: asset.Digest}
		switch asset.Name {
		case packageName:
			release.pkg = entry
			release.PackageName = asset.Name
			release.PackageSize = asset.Size
		case "SHA256SUMS.txt":
			release.checksums = entry
		}
	}
	return release, nil
}

// releasePackageName is the archive name used by the release builds, e.g.
// proxy-manager_v1.4.0_linux_amd64.tar.gz.
func releasePackageName(version string) string {
	extension := ".tar.gz"
	if runtime.GOOS == "windows" {
		extension = ".zip"
	}
	return releasePackageRoot(version) + extension
}

func releasePackageRoot(version string) string {
	return fmt.Sprintf("proxy-manager_v%s_%s_%s", version, runtime.GOOS, runtime.GOARCH)
}

// Start begins installing version in the background. version must be the
// latest release seen by the last check, so the operator installs exactly the
// release they confirmed.
func (u *Updater) Start(ctx context.Context, version string) (UpdateStatus, error) {
	if !u.enabled {
		return UpdateStatus{}, ErrUpdateDisabled
	}
	active, err := u.store.CountRunningJobs(ctx)
	if err != nil {
		return UpdateStatus{}, err
	}
	if active > 0 {
		return UpdateStatus{}, ErrUpdateJobsActive
	}
	if err := u.begin(version); err != nil {
		return UpdateStatus{}, err
	}
	return u.snapshot(), nil
}

func (u *Updater) begin(version string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.phase == updatePhaseDownloading || u.phase == updatePhaseInstalling || u.phase == updatePhaseRestarting {
		return ErrUpdateInProgress
	}
	release := u.latest
	if release == nil || release.Version != normalizeVersion(version) {
		return ErrUpdateNotLatest
	}
	if compareVersions(release.Version, Version) <= 0 {
		return ErrUpdateCurrent
	}
	if release.pkg == nil {
		return ErrUpdateNoPackage
	}
	u.phase = updatePhaseDownloading
	u.target = release.Version
	u.failure = ""
	u.downloaded.Store(0)
	u.total.Store(release.pkg.Size)
	go u.run(*release)
	return nil
}

func (u *Updater) setPhase(phase string) {
	u.mu.Lock()
	u.phase = phase
	u.mu.Unlock()
}

func (u *Updater) run(release ReleaseInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	u.logger.Info("update started", "from", Version, "to", release.Version, "package", release.pkg.Name)
	if err := u.install(ctx, release); err != nil {
		u.logger.Error("update failed", "to", release.Version, "error", sanitizeMessage(err.Error()))
		u.mu.Lock()
		u.phase = updatePhaseFailed
		u.failure = err.Error()
		u.mu.Unlock()
		return
	}
	u.logger.Info("update installed; restarting", "to", release.Version)
	u.setPhase(updatePhaseRestarting)
	// Jobs may have started during the download. Shutting down cancels them
	// (they are queued again on start), so give them a while to finish.
	// The pause also lets the console observe the restarting phase.
	deadline := time.Now().Add(restartJobGrace)
	for {
		time.Sleep(1500 * time.Millisecond)
		active, err := u.store.CountRunningJobs(ctx)
		if err != nil || active == 0 || time.Now().After(deadline) {
			break
		}
	}
	select {
	case u.restart <- struct{}{}:
	default:
	}
}

func (u *Updater) install(ctx context.Context, release ReleaseInfo) error {
	downloads := filepath.Join(u.dir, "downloads")
	if err := os.MkdirAll(downloads, 0o700); err != nil {
		return fmt.Errorf("The releases directory is not writable: %w", err)
	}
	expected, err := u.expectedChecksum(ctx, release)
	if err != nil {
		return err
	}
	archive, err := os.CreateTemp(downloads, release.pkg.Name+".*.part")
	if err != nil {
		return fmt.Errorf("The releases directory is not writable: %w", err)
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	hash := sha256.New()
	if err := u.download(ctx, release.pkg.URL, maxPackageBytes, io.MultiWriter(archive, hash), &u.downloaded); err != nil {
		return err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		return errors.New("The downloaded package does not match its SHA-256 checksum")
	}

	u.setPhase(updatePhaseInstalling)
	staging, err := os.MkdirTemp(u.dir, ".staging-")
	if err != nil {
		return fmt.Errorf("The releases directory is not writable: %w", err)
	}
	defer os.RemoveAll(staging)
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if strings.HasSuffix(release.pkg.Name, ".zip") {
		info, err := archive.Stat()
		if err != nil {
			return err
		}
		err = extractZip(archive, info.Size(), releasePackageRoot(release.Version), staging)
		if err != nil {
			return err
		}
	} else if err := extractTarGz(archive, releasePackageRoot(release.Version), staging); err != nil {
		return err
	}
	if err := verifyReleaseDir(ctx, staging, release.Version); err != nil {
		return err
	}
	if err := u.backupDatabase(ctx); err != nil {
		return err
	}
	target := releaseDir(u.dir, release.Version)
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("Remove the previous copy of the release: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return fmt.Errorf("Install the release: %w", err)
	}
	// The running release is the rollback target; the built-in program is
	// represented by an empty Previous.
	previous := ""
	if os.Getenv(releaseDirEnv) != "" {
		previous = Version
	}
	state := releaseState{Current: release.Version, Previous: previous, Pending: true, UpdatedAt: time.Now().UTC()}
	if err := writeReleaseState(u.dir, state); err != nil {
		return fmt.Errorf("Save the release state: %w", err)
	}
	removeOldReleases(u.dir, release.Version, previous)
	return nil
}

// removeOldReleases deletes installed releases other than keep. A release
// that is still running cannot be removed on Windows; it is retried after
// the next upgrade.
func removeOldReleases(root string, keep ...string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		version, ok := strings.CutPrefix(entry.Name(), "v")
		if !entry.IsDir() || !ok || normalizeVersion(version) == "" || slices.Contains(keep, version) {
			continue
		}
		os.RemoveAll(filepath.Join(root, entry.Name()))
	}
}

// expectedChecksum returns the SHA-256 of the package from SHA256SUMS.txt,
// cross-checked against the digest GitHub records for the asset.
func (u *Updater) expectedChecksum(ctx context.Context, release ReleaseInfo) (string, error) {
	digest := ""
	if value, ok := strings.CutPrefix(strings.ToLower(release.pkg.Digest), "sha256:"); ok && len(value) == 64 {
		digest = value
	}
	fromFile := ""
	if release.checksums != nil {
		var buffer strings.Builder
		if err := u.download(ctx, release.checksums.URL, maxChecksumBytes, &buffer, nil); err != nil {
			return "", err
		}
		fromFile = checksumFor(buffer.String(), release.pkg.Name)
	}
	switch {
	case fromFile == "" && digest == "":
		return "", errors.New("The release does not publish a checksum for this package")
	case fromFile != "" && digest != "" && fromFile != digest:
		return "", errors.New("The release checksums are inconsistent")
	case fromFile != "":
		return fromFile, nil
	default:
		return digest, nil
	}
}

func checksumFor(sums, name string) string {
	scanner := bufio.NewScanner(strings.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name && len(fields[0]) == 64 {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

func (u *Updater) download(ctx context.Context, url string, limit int64, destination io.Writer, progress *atomic.Int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.New("The release download address is invalid")
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "Proxy-Manager/"+Version)
	response, err := u.client.Do(request)
	if err != nil {
		return errors.New("The release could not be downloaded from GitHub")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("The release download failed with HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return errors.New("The release package is too large")
	}
	reader := io.LimitReader(response.Body, limit+1)
	buffer := make([]byte, 64<<10)
	var written int64
	for {
		count, readErr := reader.Read(buffer)
		if count > 0 {
			written += int64(count)
			if written > limit {
				return errors.New("The release package is too large")
			}
			if _, err := destination.Write(buffer[:count]); err != nil {
				return fmt.Errorf("Save the release package: %w", err)
			}
			if progress != nil {
				progress.Store(written)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errors.New("The release download was interrupted")
		}
	}
}

// archivePath maps an archive entry below root to a relative path, rejecting
// entries outside root or with unsafe components.
func archivePath(name, root string) (string, bool) {
	name = strings.TrimPrefix(strings.ReplaceAll(name, "\\", "/"), "./")
	relative, ok := strings.CutPrefix(name, root+"/")
	if !ok {
		return "", name == root
	}
	relative = strings.TrimSuffix(relative, "/")
	if relative == "" {
		return "", true
	}
	cleaned := path.Clean(relative)
	if cleaned != relative || strings.HasPrefix(cleaned, "../") || cleaned == ".." || path.IsAbs(cleaned) || strings.Contains(cleaned, ":") {
		return "", false
	}
	return filepath.FromSlash(cleaned), true
}

func extractTarGz(source io.Reader, root, destination string) error {
	gzipReader, err := gzip.NewReader(source)
	if err != nil {
		return errors.New("The release package is not a valid archive")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("The release package is not a valid archive")
		}
		relative, ok := archivePath(header.Name, root)
		if !ok {
			return errors.New("The release package contains an unexpected path")
		}
		if relative == "" {
			continue
		}
		target := filepath.Join(destination, relative)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += header.Size
			if total > maxExtractedBytes {
				return errors.New("The release package is too large")
			}
			mode := os.FileMode(0o644)
			if header.Mode&0o111 != 0 {
				mode = 0o755
			}
			if err := writeExtractedFile(target, reader, header.Size, mode); err != nil {
				return err
			}
		}
	}
}

func extractZip(source io.ReaderAt, size int64, root, destination string) error {
	reader, err := zip.NewReader(source, size)
	if err != nil {
		return errors.New("The release package is not a valid archive")
	}
	var total int64
	for _, file := range reader.File {
		relative, ok := archivePath(file.Name, root)
		if !ok {
			return errors.New("The release package contains an unexpected path")
		}
		if relative == "" {
			continue
		}
		target := filepath.Join(destination, relative)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !file.Mode().IsRegular() {
			continue
		}
		total += int64(file.UncompressedSize64)
		if total > maxExtractedBytes {
			return errors.New("The release package is too large")
		}
		content, err := file.Open()
		if err != nil {
			return errors.New("The release package is not a valid archive")
		}
		err = writeExtractedFile(target, content, int64(file.UncompressedSize64), 0o755)
		content.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeExtractedFile(target string, content io.Reader, size int64, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(content, size+1))
	closeErr := file.Close()
	if copyErr != nil || written != size {
		return errors.New("The release package is not a valid archive")
	}
	return closeErr
}

// verifyReleaseDir checks that an extracted release is complete and that its
// program runs on this machine and reports the expected version.
func verifyReleaseDir(ctx context.Context, dir, version string) error {
	for _, name := range []string{releaseBinaryName(), filepath.Join("web", "index.html"), "3proxy-install.sh"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("The release package is incomplete: %s is missing", filepath.ToSlash(name))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(dir, releaseBinaryName()), "version")
	// Should the program ignore the version argument and start serving, it
	// gets a throwaway database and no SSH access.
	command.Env = append(withoutEnv(os.Environ(), releaseDirEnv, launcherEnv, "DB_PATH", "HTTP_ADDR", "EXECUTOR_MODE", "APP_ENV", "UPDATE_ENABLED"),
		"DB_PATH=:memory:", "HTTP_ADDR=127.0.0.1:0", "EXECUTOR_MODE=mock", "APP_ENV=test", "UPDATE_ENABLED=false")
	output, err := command.Output()
	if err != nil {
		return errors.New("The new program cannot run on this server")
	}
	if reported := strings.TrimSpace(string(output)); reported != version {
		return fmt.Errorf("The new program reports version %q instead of %q", reported, version)
	}
	return nil
}

// backupDatabase writes a consistent copy of the database before the new
// release migrates it, keeping the newest databaseBackupsKept copies.
func (u *Updater) backupDatabase(ctx context.Context) error {
	backups := filepath.Join(u.dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		return fmt.Errorf("Create the backups directory: %w", err)
	}
	name := fmt.Sprintf("proxy-manager-v%s-%s.db", Version, time.Now().UTC().Format("20060102-150405"))
	if err := u.store.Backup(ctx, filepath.Join(backups, name)); err != nil {
		return fmt.Errorf("Back up the database: %w", err)
	}
	entries, err := os.ReadDir(backups)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "proxy-manager-v") && strings.HasSuffix(entry.Name(), ".db") {
			names = append(names, entry.Name())
		}
	}
	slices.SortFunc(names, func(left, right string) int {
		return compareBackupNames(left, right)
	})
	for len(names) > databaseBackupsKept {
		os.Remove(filepath.Join(backups, names[0]))
		names = names[1:]
	}
	return nil
}

// compareBackupNames orders backups by the timestamp at the end of the name.
func compareBackupNames(left, right string) int {
	stamp := func(name string) string {
		name = strings.TrimSuffix(name, ".db")
		if len(name) < 15 {
			return name
		}
		return name[len(name)-15:]
	}
	return strings.Compare(stamp(left), stamp(right))
}

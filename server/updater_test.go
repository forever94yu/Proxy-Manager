package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"1.4.0", "1.3.0", 1},
		{"v1.3.0", "1.3.0", 0},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "10.0.0", -1},
		{"1.4.0-rc.1", "1.4.0", -1},
		{"1.4.0-rc.2", "1.4.0-rc.1", 1},
		{"garbage", "0.0.1", -1},
		{"1.3", "1.3.0", -1},
	}
	for _, testCase := range cases {
		if got := compareVersions(testCase.left, testCase.right); got != testCase.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", testCase.left, testCase.right, got, testCase.want)
		}
	}
	if normalizeVersion("v1.4.0") != "1.4.0" || normalizeVersion("1.4") != "" || normalizeVersion("v01.4.0") != "" {
		t.Fatal("normalizeVersion accepted or rejected the wrong values")
	}
}

// The release version is kept in package.json and version.go; release builds
// and the online upgrade rely on them being equal.
func TestVersionMatchesPackageJSON(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != Version {
		t.Fatalf("package.json version %q differs from Version %q", manifest.Version, Version)
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := "deadbeef  other.tar.gz\n" + strings.ToUpper(sum) + " *proxy-manager_v1.4.0_linux_amd64.tar.gz\r\n"
	if got := checksumFor(sums, "proxy-manager_v1.4.0_linux_amd64.tar.gz"); got != sum {
		t.Fatalf("checksumFor = %q", got)
	}
	if checksumFor(sums, "missing.zip") != "" || checksumFor(sums, "other.tar.gz") != "" {
		t.Fatal("checksumFor returned a checksum for a missing or malformed entry")
	}
}

func TestArchivePathRejectsEscapes(t *testing.T) {
	root := "proxy-manager_v1.4.0_linux_amd64"
	valid := map[string]string{
		root + "/":                     "",
		root:                           "",
		root + "/web/index.html":       filepath.Join("web", "index.html"),
		"./" + root + "/proxy-manager": "proxy-manager",
	}
	for name, want := range valid {
		if got, ok := archivePath(name, root); !ok || got != want {
			t.Errorf("archivePath(%q) = %q, %v", name, got, ok)
		}
	}
	for _, name := range []string{
		root + "/../evil", root + "/web/../../evil", "/etc/passwd", "other/file", root + "/c:/evil", root + "/a//b",
	} {
		if _, ok := archivePath(name, root); ok {
			t.Errorf("archivePath(%q) accepted an unsafe path", name)
		}
	}
}

func TestReleaseStateRoundTripAndInstalledRelease(t *testing.T) {
	root := t.TempDir()
	if _, dir := installedRelease(root); dir != "" {
		t.Fatal("installedRelease found a release without a state file")
	}
	newer := bumpVersion(Version)
	if err := writeReleaseState(root, releaseState{Current: newer, Pending: true}); err != nil {
		t.Fatal(err)
	}
	if _, dir := installedRelease(root); dir != "" {
		t.Fatal("installedRelease accepted a release whose program is missing")
	}
	binary := filepath.Join(releaseDir(root, newer), releaseBinaryName())
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	state, dir := installedRelease(root)
	if dir != releaseDir(root, newer) || !state.Pending {
		t.Fatalf("installedRelease = %+v, %q", state, dir)
	}
	if err := writeReleaseState(root, releaseState{Current: Version}); err != nil {
		t.Fatal(err)
	}
	if _, dir := installedRelease(root); dir != "" {
		t.Fatal("installedRelease offered a release that is not newer than the running program")
	}
}

func TestUpdaterStatusAndInstall(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a release program")
	}
	newVersion := bumpVersion(Version)
	binary := buildVersionBinary(t, newVersion)
	archive := releaseArchive(t, newVersion, binary)
	sum := sha256.Sum256(archive)
	github := fakeGitHub(t, newVersion, archive, hex.EncodeToString(sum[:]))

	app := newTestApplication(t, nil)
	updater := app.api.updater
	updater.enabled = true
	updater.repository = defaultUpdateRepository
	updater.dir = filepath.Join(t.TempDir(), "releases")
	updater.apiURL = github.URL
	loginTestClient(t, app)

	response := requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/system/update", nil)
	assertStatus(t, response, http.StatusOK)
	var status UpdateStatus
	decodeUpdateStatus(t, response, &status)
	if status.CurrentVersion != Version || !status.UpdateAvailable || status.Latest == nil ||
		status.Latest.Version != newVersion || status.Latest.PackageName != releasePackageName(newVersion) {
		t.Fatalf("unexpected update status: %+v", status)
	}

	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/system/update", map[string]string{"version": "0.0.1"})
	assertStatus(t, response, http.StatusConflict)
	response.Body.Close()

	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/system/update", map[string]string{"version": newVersion})
	assertStatus(t, response, http.StatusAccepted)
	response.Body.Close()

	select {
	case <-updater.RestartRequested():
	case <-time.After(30 * time.Second):
		t.Fatalf("update did not finish: %+v", updater.snapshot())
	}
	if phase := updater.snapshot().Phase; phase != updatePhaseRestarting {
		t.Fatalf("phase = %q, want restarting", phase)
	}
	state, dir := installedRelease(updater.dir)
	if dir == "" || state.Current != newVersion || !state.Pending || state.Previous != "" {
		t.Fatalf("release state after install = %+v, %q", state, dir)
	}
	for _, name := range []string{releaseBinaryName(), "3proxy-install.sh", filepath.Join("web", "index.html")} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("installed release lacks %s: %v", name, err)
		}
	}
	backups, err := os.ReadDir(filepath.Join(updater.dir, "backups"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one database backup, got %v (%v)", backups, err)
	}
}

func TestUpdaterRejectsChecksumMismatch(t *testing.T) {
	newVersion := bumpVersion(Version)
	archive := releaseArchive(t, newVersion, []byte("not a program"))
	github := fakeGitHub(t, newVersion, archive, strings.Repeat("00", 32))
	app := newTestApplication(t, nil)
	updater := app.api.updater
	updater.enabled = true
	updater.repository = defaultUpdateRepository
	updater.dir = filepath.Join(t.TempDir(), "releases")
	updater.apiURL = github.URL

	updater.Status(context.Background(), true)
	if _, err := updater.Start(context.Background(), newVersion); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for updater.snapshot().Phase != updatePhaseFailed {
		if time.Now().After(deadline) {
			t.Fatalf("update did not fail: %+v", updater.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status := updater.snapshot(); !strings.Contains(status.Error, "checksum") {
		t.Fatalf("unexpected failure: %q", status.Error)
	}
	if _, dir := installedRelease(updater.dir); dir != "" {
		t.Fatal("a release with a bad checksum was installed")
	}
}

func TestUpdateEndpointsRequireLogin(t *testing.T) {
	app := newTestApplication(t, nil)
	response := requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/system/update", nil)
	assertStatus(t, response, http.StatusUnauthorized)
	response.Body.Close()
	loginTestClient(t, app)
	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/system/update", nil)
	assertStatus(t, response, http.StatusOK)
	var status UpdateStatus
	decodeUpdateStatus(t, response, &status)
	if status.Enabled || status.CurrentVersion != Version {
		t.Fatalf("unexpected status without an update directory: %+v", status)
	}
	response = requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/system/update", map[string]string{"version": "9.9.9"})
	assertStatus(t, response, http.StatusUnprocessableEntity)
	response.Body.Close()
}

func TestLauncherRollsBackAFailedRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a release program")
	}
	root := t.TempDir()
	newer := bumpVersion(Version)
	program := filepath.Join(releaseDir(root, newer), releaseBinaryName())
	if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
		t.Fatal(err)
	}
	// Stands in for a release that exits before it is ready.
	source := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(source, []byte("package main\n\nimport \"os\"\n\nfunc main() { os.Exit(3) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", program, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, combined)
	}
	if err := writeReleaseState(root, releaseState{Current: newer, Pending: true}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, ran := runInstalledRelease(root, logger); ran {
		t.Fatal("launcher kept running a release that failed to start")
	}
	state, err := readReleaseState(root)
	if err != nil || state.Current != "" || state.FailedVersion != newer || state.Pending {
		t.Fatalf("state after rollback = %+v (%v)", state, err)
	}

	// A release that started once is not rolled back when it exits later;
	// its exit code is passed on.
	if err := writeReleaseState(root, releaseState{Current: newer}); err != nil {
		t.Fatal(err)
	}
	if code, ran := runInstalledRelease(root, logger); !ran || code != 3 {
		t.Fatalf("runInstalledRelease = %d, %v; want 3, true", code, ran)
	}
	if state, _ := readReleaseState(root); state.Current != newer {
		t.Fatalf("a healthy release was rolled back: %+v", state)
	}
}

func bumpVersion(version string) string {
	parsed, _ := parseVersion(version)
	return fmt.Sprintf("%d.%d.%d", parsed.core[0], parsed.core[1]+1, 0)
}

// buildVersionBinary builds this package with Version overridden, standing in
// for the program of a newer release.
func buildVersionBinary(t *testing.T, version string) []byte {
	t.Helper()
	output := filepath.Join(t.TempDir(), releaseBinaryName())
	command := exec.Command("go", "build", "-o", output, "-ldflags", "-X main.Version="+version, ".")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, combined)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func releaseArchive(t *testing.T, version string, binary []byte) []byte {
	t.Helper()
	root := releasePackageRoot(version)
	files := []struct {
		name    string
		content []byte
		mode    int64
	}{
		{releaseBinaryName(), binary, 0o755},
		{"3proxy-install.sh", []byte("#!/bin/sh\n"), 0o755},
		{"web/index.html", []byte("<!doctype html>"), 0o644},
	}
	var buffer bytes.Buffer
	if runtime.GOOS == "windows" {
		writer := zip.NewWriter(&buffer)
		for _, file := range files {
			entry, err := writer.Create(root + "/" + file.name)
			if err != nil {
				t.Fatal(err)
			}
			entry.Write(file.content)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return buffer.Bytes()
	}
	gzipWriter := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gzipWriter)
	writer.WriteHeader(&tar.Header{Name: root + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	writer.WriteHeader(&tar.Header{Name: root + "/web/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, file := range files {
		if err := writer.WriteHeader(&tar.Header{Name: root + "/" + file.name, Typeflag: tar.TypeReg, Mode: file.mode, Size: int64(len(file.content))}); err != nil {
			t.Fatal(err)
		}
		writer.Write(file.content)
	}
	writer.Close()
	gzipWriter.Close()
	return buffer.Bytes()
}

func fakeGitHub(t *testing.T, version string, archive []byte, checksum string) *httptest.Server {
	t.Helper()
	packageName := releasePackageName(version)
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("GET /repos/forever94yu/Proxy-Manager/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v" + version, "name": "Proxy Manager v" + version, "body": "notes",
			"html_url": "https://example.invalid/release", "published_at": time.Now().UTC().Format(time.RFC3339),
			"assets": []map[string]any{
				{"name": packageName, "size": len(archive), "browser_download_url": server.URL + "/download/" + packageName},
				{"name": "SHA256SUMS.txt", "size": 100, "browser_download_url": server.URL + "/download/SHA256SUMS.txt"},
			},
		})
	})
	mux.HandleFunc("GET /download/"+packageName, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	})
	mux.HandleFunc("GET /download/SHA256SUMS.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", checksum, packageName)
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func decodeUpdateStatus(t *testing.T, response *http.Response, status *UpdateStatus) {
	t.Helper()
	var envelope struct {
		Data UpdateStatus `json:"data"`
	}
	decodeBody(t, readBody(t, response), &envelope)
	*status = envelope.Data
}

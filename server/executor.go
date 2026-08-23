package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const maxCommandOutput = 1 << 20

type ExecutionRequest struct {
	Server     Server
	Credential string
	Task       TargetTask
	Password   string
}

type ExecutionResult struct {
	Message string
	Update  ExecutionUpdate
}

type Executor interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
}

type MockExecutor struct {
	Delay time.Duration
}

func (m *MockExecutor) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	delay := m.Delay
	if delay <= 0 {
		delay = 20 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ExecutionResult{}, ctx.Err()
	case <-timer.C:
	}
	if mockShouldFail(request.Server, request.Task.Action) {
		return ExecutionResult{Update: ExecutionUpdate{ServerStatus: "offline"}},
			fmt.Errorf("mock executor rejected %s on %s", request.Task.Action, request.Server.Name)
	}
	result := ExecutionResult{
		Message: "Mock operation completed",
		Update: ExecutionUpdate{
			ServerStatus: "online",
			OS:           "linux (mock)",
			PublicIP:     request.Server.Host,
		},
	}
	switch request.Task.Action {
	case "inspect":
		result.Message = "Connection and inspection succeeded"
		if request.Server.ServiceStatus == "unknown" {
			result.Update.ServiceStatus = "stopped"
		}
	case "deploy":
		result.Message = "3proxy deployment succeeded"
		result.Update.InstallStatus = "installed"
		result.Update.ServiceStatus = "running"
		result.Update.Version = "mock-3proxy"
	case "service":
		result.Message = "Service " + request.Task.ServiceAction + " succeeded"
		switch request.Task.ServiceAction {
		case "start", "restart":
			result.Update.ServiceStatus = "running"
		case "stop":
			result.Update.ServiceStatus = "stopped"
		case "status":
			result.Update.ServiceStatus = request.Server.ServiceStatus
		}
	case "user-add", "user-update", "user-delete":
		result.Message = "Proxy user synchronization succeeded"
	}
	return result, nil
}

func mockShouldFail(server Server, action string) bool {
	lowerHost := strings.ToLower(server.Host)
	if strings.HasPrefix(lowerHost, "fail.") || strings.Contains(lowerHost, ".invalid") {
		return true
	}
	for _, tag := range server.Tags {
		lowerTag := strings.ToLower(tag)
		if lowerTag == "mock:fail" || lowerTag == "mock:fail:"+action {
			return true
		}
	}
	return false
}

type SSHExecutor struct {
	ScriptPath     string
	ConnectTimeout time.Duration
	CommandTimeout time.Duration
}

func (e *SSHExecutor) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	script, err := os.ReadFile(e.ScriptPath)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("read installer script: %w", err)
	}
	authMethod, err := sshAuthMethod(request.Server.AuthMethod, request.Credential)
	if err != nil {
		return ExecutionResult{}, err
	}
	var observedFingerprint string
	hostKeyCallback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		observedFingerprint = ssh.FingerprintSHA256(key)
		if request.Server.HostFingerprint == "" {
			return nil
		}
		if request.Server.HostFingerprint != observedFingerprint {
			return fmt.Errorf("SSH host key mismatch: expected %s, received %s", request.Server.HostFingerprint, observedFingerprint)
		}
		return nil
	}
	clientConfig := &ssh.ClientConfig{
		User:            request.Server.SSHUser,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: hostKeyCallback,
		Timeout:         e.ConnectTimeout,
	}
	address := net.JoinHostPort(strings.Trim(request.Server.Host, "[]"), strconv.Itoa(request.Server.SSHPort))
	client, err := ssh.Dial("tcp", address, clientConfig)
	if err != nil {
		return ExecutionResult{Update: ExecutionUpdate{ServerStatus: "offline"}},
			fmt.Errorf("SSH connection failed: %w", err)
	}
	defer client.Close()
	connectedUpdate := ExecutionUpdate{HostFingerprint: observedFingerprint, ServerStatus: "online"}

	remotePath, err := uploadScriptSCP(ctx, client, script, e.CommandTimeout)
	if err != nil {
		return ExecutionResult{Update: connectedUpdate}, err
	}
	defer cleanupRemoteFile(client, remotePath, 10*time.Second)
	if !bytes.Contains(script, []byte("--api")) {
		return ExecutionResult{
			Update: connectedUpdate,
		}, errors.New("uploaded installer does not implement the required --api command contract")
	}

	output, err := runRemoteTask(ctx, client, remotePath, request, e.CommandTimeout)
	output = redactSecrets(output, request.Password)
	if err != nil {
		return ExecutionResult{Update: connectedUpdate},
			fmt.Errorf("remote command failed: %s", sanitizeMessage(output+": "+err.Error()))
	}
	result := ExecutionResult{
		Message: sanitizeMessage(output),
		Update: ExecutionUpdate{
			HostFingerprint: observedFingerprint,
			ServerStatus:    "online",
		},
	}
	applyRemoteResult(&result, request, output)
	if result.Message == "" {
		result.Message = "Remote operation completed"
	}
	return result, nil
}

func runRemoteTask(ctx context.Context, client *ssh.Client, path string, request ExecutionRequest, timeout time.Duration) (string, error) {
	output, err := runRemoteTaskOnce(ctx, client, path, request, timeout)
	if err == nil {
		return output, nil
	}
	exitCode, ok := sshExitCode(err)
	if !ok {
		return output, err
	}
	switch request.Task.Action {
	case "user-delete":
		if exitCode == 4 {
			return "PM\tresult\talready_absent", nil
		}
	case "user-add":
		if exitCode == 3 {
			update := request
			update.Task.Action = "user-update"
			update.Task.OldUsername = request.Task.Username
			return runRemoteTaskOnce(ctx, client, path, update, timeout)
		}
	case "user-update":
		switch exitCode {
		case 4:
			// The old name may already have been renamed before an ACK was lost.
			updateDesired := request
			updateDesired.Task.OldUsername = request.Task.Username
			updatedOutput, updatedErr := runRemoteTaskOnce(ctx, client, path, updateDesired, timeout)
			if updatedErr == nil {
				return updatedOutput, nil
			}
			if code, codeOK := sshExitCode(updatedErr); !codeOK || code != 4 {
				return updatedOutput, updatedErr
			}
			addDesired := request
			addDesired.Task.Action = "user-add"
			addedOutput, addedErr := runRemoteTaskOnce(ctx, client, path, addDesired, timeout)
			if code, codeOK := sshExitCode(addedErr); codeOK && code == 3 {
				return runRemoteTaskOnce(ctx, client, path, updateDesired, timeout)
			}
			return addedOutput, addedErr
		case 3:
			// Both old and desired names exist. Converge the desired record first,
			// then remove the obsolete name.
			updateDesired := request
			updateDesired.Task.OldUsername = request.Task.Username
			updatedOutput, updatedErr := runRemoteTaskOnce(ctx, client, path, updateDesired, timeout)
			if updatedErr != nil {
				return updatedOutput, updatedErr
			}
			deleteOld := request
			deleteOld.Task.Action = "user-delete"
			deleteOld.Task.Username = request.Task.OldUsername
			deletedOutput, deletedErr := runRemoteTaskOnce(ctx, client, path, deleteOld, timeout)
			if code, codeOK := sshExitCode(deletedErr); codeOK && code == 4 {
				deletedErr = nil
			}
			return updatedOutput + "\n" + deletedOutput, deletedErr
		}
	}
	return output, err
}

func runRemoteTaskOnce(ctx context.Context, client *ssh.Client, path string, request ExecutionRequest, timeout time.Duration) (string, error) {
	arguments, stdin, err := remoteArguments(path, request)
	if err != nil {
		return "", err
	}
	return runSSHCommand(ctx, client, shellJoin(arguments), stdin, timeout)
}

func sshExitCode(err error) (int, bool) {
	var exitError *ssh.ExitError
	if !errors.As(err, &exitError) {
		return 0, false
	}
	return exitError.ExitStatus(), true
}

func sshAuthMethod(method, credential string) (ssh.AuthMethod, error) {
	switch method {
	case "password":
		return ssh.Password(credential), nil
	case "key":
		signer, err := ssh.ParsePrivateKey([]byte(credential))
		if err != nil {
			return nil, errors.New("SSH private key cannot be parsed or requires an unsupported passphrase")
		}
		return ssh.PublicKeys(signer), nil
	default:
		return nil, errors.New("unsupported SSH authentication method")
	}
}

func uploadScriptSCP(ctx context.Context, client *ssh.Client, script []byte, timeout time.Duration) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	filename := "proxy-manager-" + hex.EncodeToString(random) + ".sh"
	remotePath := "/tmp/" + filename
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("create SCP session: %w", err)
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("open SCP input: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("open SCP output: %w", err)
	}
	var stderr cappedBuffer
	session.Stderr = &stderr
	if err := session.Start("scp -qt /tmp"); err != nil {
		return "", fmt.Errorf("start SCP upload: %w", err)
	}
	reader := bufio.NewReader(stdout)
	done := make(chan error, 1)
	go func() {
		if err := readSCPAck(reader); err != nil {
			done <- err
			return
		}
		if _, err := fmt.Fprintf(stdin, "C0700 %d %s\n", len(script), filename); err != nil {
			done <- err
			return
		}
		if err := readSCPAck(reader); err != nil {
			done <- err
			return
		}
		if _, err := stdin.Write(script); err != nil {
			done <- err
			return
		}
		if _, err := stdin.Write([]byte{0}); err != nil {
			done <- err
			return
		}
		if err := readSCPAck(reader); err != nil {
			done <- err
			return
		}
		stdin.Close()
		done <- session.Wait()
	}()
	wait := timeout
	if wait <= 0 {
		wait = time.Minute
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		session.Close()
		return "", ctx.Err()
	case <-timer.C:
		session.Close()
		return "", errors.New("SCP upload timed out")
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("SCP upload failed: %s: %w", sanitizeMessage(stderr.String()), err)
		}
		return remotePath, nil
	}
}

func readSCPAck(reader *bufio.Reader) error {
	code, err := reader.ReadByte()
	if err != nil {
		return err
	}
	switch code {
	case 0:
		return nil
	case 1, 2:
		message, _ := reader.ReadString('\n')
		return errors.New(strings.TrimSpace(message))
	default:
		return fmt.Errorf("unexpected SCP response code %d", code)
	}
}

func remoteArguments(path string, request ExecutionRequest) ([]string, string, error) {
	arguments := []string{"sudo", "-n", "bash", path, "--api"}
	if request.Server.SSHUser == "root" {
		arguments = []string{"bash", path, "--api"}
	}
	stdin := ""
	switch request.Task.Action {
	case "inspect":
		arguments = append(arguments, "inspect")
	case "deploy":
		dns := append([]string(nil), request.Task.DNS...)
		if len(dns) == 1 {
			dns = append(dns, dns[0])
		}
		if len(dns) != 2 {
			return nil, "", errors.New("deploy requires one or two DNS resolvers")
		}
		arguments = append(arguments, "deploy", request.Task.PublicIP,
			strconv.Itoa(request.Task.HTTPPort), strconv.Itoa(request.Task.SocksPort), dns[0], dns[1])
	case "user-add":
		arguments = append(arguments, "user-add", request.Task.Username)
		stdin = request.Password + "\n"
	case "user-update":
		arguments = append(arguments, "user-update", request.Task.OldUsername, request.Task.Username)
		stdin = request.Password + "\n"
	case "user-delete":
		arguments = append(arguments, "user-delete", request.Task.Username)
	case "service":
		arguments = append(arguments, "service", request.Task.ServiceAction)
	default:
		return nil, "", errors.New("unsupported remote operation")
	}
	return arguments, stdin, nil
}

func runSSHCommand(ctx context.Context, client *ssh.Client, command, stdin string, timeout time.Duration) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var output cappedBuffer
	session.Stdout = &output
	session.Stderr = &output
	if stdin != "" {
		session.Stdin = strings.NewReader(stdin)
	}
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	wait := timeout
	if wait <= 0 {
		wait = 15 * time.Minute
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		session.Close()
		waitSSHSession(done)
		return output.String(), ctx.Err()
	case <-timer.C:
		session.Close()
		waitSSHSession(done)
		return output.String(), errors.New("remote command timed out")
	case err := <-done:
		return output.String(), err
	}
}

func waitSSHSession(done <-chan error) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func cleanupRemoteFile(client *ssh.Client, path string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = runSSHCommand(ctx, client, "rm -f -- "+shellQuote(path), "", timeout)
}

func shellJoin(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = shellQuote(argument)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func applyRemoteResult(result *ExecutionResult, request ExecutionRequest, output string) {
	var report struct {
		OS            string `json:"os"`
		Version       string `json:"version"`
		PublicIP      string `json:"publicIp"`
		ServiceStatus string `json:"serviceStatus"`
		InstallStatus string `json:"installStatus"`
	}
	if json.Unmarshal([]byte(output), &report) == nil {
		result.Update.OS = report.OS
		result.Update.Version = report.Version
		result.Update.PublicIP = report.PublicIP
		result.Update.ServiceStatus = report.ServiceStatus
		result.Update.InstallStatus = report.InstallStatus
	}
	pmValues := parsePMOutput(output)
	if value := pmValues["os"]; value != "" {
		result.Update.OS = value
	}
	if value := pmValues["version"]; value != "" {
		result.Update.Version = value
	}
	if value := pmValues["server_ip"]; value != "" {
		result.Update.PublicIP = value
	}
	if value := firstNonEmpty(pmValues["installed"], pmValues["install_status"]); value != "" {
		switch strings.ToLower(value) {
		case "1", "true", "yes", "installed":
			result.Update.InstallStatus = "installed"
		case "0", "false", "no", "not_installed", "missing":
			result.Update.InstallStatus = "not_installed"
		case "failed":
			result.Update.InstallStatus = "failed"
		}
	}
	if value := firstNonEmpty(pmValues["service"], pmValues["service_status"]); value != "" {
		switch strings.ToLower(value) {
		case "active", "running", "started":
			result.Update.ServiceStatus = "running"
		case "inactive", "stopped", "dead":
			result.Update.ServiceStatus = "stopped"
		case "failed":
			result.Update.ServiceStatus = "failed"
		default:
			result.Update.ServiceStatus = "unknown"
		}
	}
	switch request.Task.Action {
	case "deploy":
		result.Update.InstallStatus = "installed"
		result.Update.ServiceStatus = "running"
	case "service":
		switch request.Task.ServiceAction {
		case "start", "restart":
			result.Update.ServiceStatus = "running"
		case "stop":
			result.Update.ServiceStatus = "stopped"
		}
	}
}

func parsePMOutput(output string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) < 3 || parts[0] != "PM" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[1]))
		if key == "" {
			continue
		}
		values[key] = strings.TrimSpace(strings.Join(parts[2:], "\t"))
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type cappedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	originalLength := len(p)
	remaining := maxCommandOutput - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return originalLength, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return originalLength, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := b.buffer.String()
	if b.truncated {
		value += "\n[output truncated]"
	}
	return value
}

func redactSecrets(value string, secrets ...string) string {
	redacted := value
	for _, secret := range secrets {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "[REDACTED]")
		}
	}
	lines := strings.Split(redacted, "\n")
	for index, line := range lines {
		if strings.Contains(line, "PRIVATE KEY") || strings.Contains(line, ":CL:") {
			lines[index] = "[REDACTED]"
		}
	}
	return strings.Join(lines, "\n")
}

func sanitizeMessage(value string) string {
	value = strings.TrimSpace(redactSecrets(value))
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 {
			return r
		}
		return -1
	}, value)
	if len(value) > 1000 {
		value = value[:1000] + "..."
	}
	return value
}

var _ io.Writer = (*cappedBuffer)(nil)

package main

import (
	"bytes"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecretBoxUsesAuthenticatedContext(t *testing.T) {
	box, err := NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("sensitive-value")
	ciphertext, err := box.Encrypt(plaintext, "context-a")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(ciphertext), plaintext) {
		t.Fatal("ciphertext contains plaintext")
	}
	decrypted, err := box.Decrypt(ciphertext, "context-a")
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("Decrypt = %q, %v", decrypted, err)
	}
	if _, err := box.Decrypt(ciphertext, "context-b"); err == nil {
		t.Fatal("decrypt with different AAD context succeeded")
	}
}

func TestProductionConfigRequiresExplicitSecrets(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("MASTER_KEY", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("production configuration accepted missing secrets")
	}
}

func TestDevelopmentConfigAllowsViteOrigins(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("MASTER_KEY", "")
	t.Setenv("CORS_ORIGINS", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:5173"} {
		if _, allowed := cfg.AllowedOrigins[origin]; !allowed {
			t.Fatalf("development origin %s is not allowed", origin)
		}
	}
}

func TestLoginClientIPOnlyTrustsConfiguredProxy(t *testing.T) {
	_, trusted, err := net.ParseCIDR("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "http://manager.example/api/v1/auth/login", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-For", "198.51.100.25, 127.0.0.2")
	if clientIP := loginClientIP(request, []*net.IPNet{trusted}); clientIP != "198.51.100.25" {
		t.Fatalf("trusted-proxy client IP = %q", clientIP)
	}
	request.RemoteAddr = "203.0.113.9:12345"
	if clientIP := loginClientIP(request, []*net.IPNet{trusted}); clientIP != "203.0.113.9" {
		t.Fatalf("untrusted peer spoofed forwarded client IP: %q", clientIP)
	}
}

func TestOriginSchemeOnlyTrustsConfiguredProxy(t *testing.T) {
	_, trusted, err := net.ParseCIDR("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	api := &API{cfg: Config{TrustedProxyCIDRs: []*net.IPNet{trusted}}}
	request := httptest.NewRequest("POST", "http://manager.example/api/v1/auth/login", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-Proto", "https")
	if !api.originAllowed(request, "https://manager.example") {
		t.Fatal("trusted proxy HTTPS origin was rejected")
	}
	request.RemoteAddr = "203.0.113.9:12345"
	if api.originAllowed(request, "https://manager.example") {
		t.Fatal("untrusted peer spoofed X-Forwarded-Proto")
	}
}

func TestRemoteArgumentsKeepPasswordOutOfCommand(t *testing.T) {
	request := ExecutionRequest{
		Server:   Server{SSHUser: "root"},
		Task:     TargetTask{Action: "user-add", Username: "alice"},
		Password: "Password123",
	}
	arguments, stdin, err := remoteArguments("/tmp/manager.sh", request)
	if err != nil {
		t.Fatal(err)
	}
	command := shellJoin(arguments)
	if bytes.Contains([]byte(command), []byte(request.Password)) {
		t.Fatal("proxy password appeared in remote command arguments")
	}
	if stdin != request.Password+"\n" {
		t.Fatalf("password stdin = %q", stdin)
	}
	if arguments[0] != "bash" {
		t.Fatalf("root SSH user should execute bash directly: %v", arguments)
	}
	request.Server.SSHUser = "deploy"
	arguments, _, err = remoteArguments("/tmp/manager.sh", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) < 3 || arguments[0] != "sudo" || arguments[1] != "-n" {
		t.Fatalf("non-root SSH user should use sudo -n: %v", arguments)
	}
}

func TestRemoteArgumentsSendPoliciesOnStdin(t *testing.T) {
	policy := NodePolicy{Username: "alice", State: NodeStateDisabled, CapMB: 512, Period: 3}
	request := ExecutionRequest{
		Server:   Server{SSHUser: "root"},
		Task:     TargetTask{Action: "user-update", OldUsername: "alice_old", Username: "alice", Policy: &policy},
		Password: "Password123",
	}
	arguments, stdin, err := remoteArguments("/tmp/manager.sh", request)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(arguments[3:], " "); got != "user-update alice_old alice" {
		t.Fatalf("arguments = %q", got)
	}
	if stdin != "Password123\ndisabled 512 3\n" {
		t.Fatalf("stdin = %q", stdin)
	}

	request.Task = TargetTask{Action: "policy-apply", Policies: []NodePolicy{
		{Username: "alice", State: NodeStateEnabled, CapMB: UnlimitedCapMB, Period: 0},
		{Username: "bob", State: NodeStateDisabled, CapMB: 1, Period: 7},
	}}
	arguments, stdin, err = remoteArguments("/tmp/manager.sh", request)
	if err != nil {
		t.Fatal(err)
	}
	if arguments[len(arguments)-1] != "policy-apply" || stdin != "alice enabled 1073741824 0\nbob disabled 1 7\n" {
		t.Fatalf("policy-apply arguments %v stdin %q", arguments, stdin)
	}

	request.Task = TargetTask{Action: "policy-apply", Policies: []NodePolicy{{Username: "bad name", State: NodeStateEnabled, CapMB: 1}}}
	if _, _, err := remoteArguments("/tmp/manager.sh", request); err == nil {
		t.Fatal("invalid policy username was accepted")
	}

	request.Task = TargetTask{Action: "traffic"}
	arguments, stdin, err = remoteArguments("/tmp/manager.sh", request)
	if err != nil || arguments[len(arguments)-1] != "traffic" || stdin != "" {
		t.Fatalf("traffic arguments %v stdin %q err %v", arguments, stdin, err)
	}
}

func TestParsePMOutputMapsInspectState(t *testing.T) {
	result := ExecutionResult{Update: ExecutionUpdate{ServerStatus: "online"}}
	request := ExecutionRequest{Task: TargetTask{Action: "inspect"}}
	output := "PM\tinstalled\tyes\nPM\tservice\tactive\nPM\tserver_ip\t203.0.113.10\nPM\tversion\t0.9.5\n"
	applyRemoteResult(&result, request, output)
	if result.Update.InstallStatus != "installed" || result.Update.ServiceStatus != "running" ||
		result.Update.PublicIP != "203.0.113.10" || result.Update.Version != "0.9.5" {
		t.Fatalf("unexpected parsed PM state: %+v", result.Update)
	}
}

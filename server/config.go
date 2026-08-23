package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	developmentAdminPassword = "ProxyManager!2026"
	developmentSessionSecret = "proxy-manager-development-session-secret-change-me"
	developmentMasterKey     = "proxy-manager-development-master-key-change-me"
)

type Config struct {
	Environment       string
	HTTPAddr          string
	DatabasePath      string
	AdminUsername     string
	AdminPassword     string
	SessionSecret     []byte
	MasterKey         []byte
	SessionTTL        time.Duration
	CookieSecure      bool
	ExecutorMode      string
	WorkerConcurrency int
	WorkerPoll        time.Duration
	SSHTimeout        time.Duration
	CommandTimeout    time.Duration
	ScriptPath        string
	StaticDir         string
	AllowedOrigins    map[string]struct{}
	TrustedProxyCIDRs []*net.IPNet
}

func LoadConfig() (Config, error) {
	environment := strings.ToLower(envOrDefault("APP_ENV", "development"))
	if environment != "development" && environment != "test" && environment != "production" {
		return Config{}, fmt.Errorf("APP_ENV must be development, test, or production")
	}

	adminPassword := os.Getenv("ADMIN_PASSWORD")
	sessionSecret := os.Getenv("SESSION_SECRET")
	masterKeyValue := os.Getenv("MASTER_KEY")
	if environment == "production" {
		missing := make([]string, 0, 3)
		if adminPassword == "" {
			missing = append(missing, "ADMIN_PASSWORD")
		}
		if sessionSecret == "" {
			missing = append(missing, "SESSION_SECRET")
		}
		if masterKeyValue == "" {
			missing = append(missing, "MASTER_KEY")
		}
		if len(missing) > 0 {
			return Config{}, fmt.Errorf("production requires explicit %s", strings.Join(missing, ", "))
		}
	} else {
		if adminPassword == "" {
			adminPassword = developmentAdminPassword
		}
		if sessionSecret == "" {
			sessionSecret = developmentSessionSecret
		}
		if masterKeyValue == "" {
			masterKeyValue = developmentMasterKey
		}
	}
	if len(adminPassword) < 8 {
		return Config{}, errors.New("ADMIN_PASSWORD must contain at least 8 characters")
	}
	if len(sessionSecret) < 32 {
		return Config{}, errors.New("SESSION_SECRET must contain at least 32 characters")
	}
	masterKey, err := parseMasterKey(masterKeyValue)
	if err != nil {
		return Config{}, err
	}

	databasePath := envOrDefault("DB_PATH", filepath.Join("data", "proxy-manager.db"))
	if databasePath != ":memory:" {
		databasePath, err = filepath.Abs(databasePath)
		if err != nil {
			return Config{}, fmt.Errorf("resolve DB_PATH: %w", err)
		}
	}
	scriptPath := envOrDefault("INSTALL_SCRIPT_PATH", filepath.Join("..", "3proxy-install.sh"))
	scriptPath, err = filepath.Abs(scriptPath)
	if err != nil {
		return Config{}, fmt.Errorf("resolve INSTALL_SCRIPT_PATH: %w", err)
	}
	staticDir := envOrDefault("STATIC_DIR", filepath.Join("..", "web", "dist"))
	staticDir, err = filepath.Abs(staticDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve STATIC_DIR: %w", err)
	}

	executorMode := strings.ToLower(envOrDefault("EXECUTOR_MODE", "mock"))
	if executorMode != "mock" && executorMode != "ssh" {
		return Config{}, errors.New("EXECUTOR_MODE must be mock or ssh")
	}
	workerConcurrency, err := envInt("WORKER_CONCURRENCY", 4, 1, 64)
	if err != nil {
		return Config{}, err
	}
	sessionTTL, err := envDuration("SESSION_TTL", 12*time.Hour)
	if err != nil {
		return Config{}, err
	}
	workerPoll, err := envDuration("WORKER_POLL_INTERVAL", 300*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	sshTimeout, err := envDuration("SSH_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	commandTimeout, err := envDuration("COMMAND_TIMEOUT", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	originsValue := os.Getenv("CORS_ORIGINS")
	if strings.TrimSpace(originsValue) == "" && environment != "production" {
		originsValue = "http://localhost:5173,http://127.0.0.1:5173"
	}
	trustedProxyCIDRs, err := parseCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return Config{}, err
	}

	return Config{
		Environment:       environment,
		HTTPAddr:          envOrDefault("HTTP_ADDR", ":8080"),
		DatabasePath:      databasePath,
		AdminUsername:     envOrDefault("ADMIN_USERNAME", "admin"),
		AdminPassword:     adminPassword,
		SessionSecret:     []byte(sessionSecret),
		MasterKey:         masterKey,
		SessionTTL:        sessionTTL,
		CookieSecure:      environment == "production" || envBool("COOKIE_SECURE", false),
		ExecutorMode:      executorMode,
		WorkerConcurrency: workerConcurrency,
		WorkerPoll:        workerPoll,
		SSHTimeout:        sshTimeout,
		CommandTimeout:    commandTimeout,
		ScriptPath:        scriptPath,
		StaticDir:         staticDir,
		AllowedOrigins:    parseOrigins(originsValue),
		TrustedProxyCIDRs: trustedProxyCIDRs,
	}, nil
}

func parseMasterKey(value string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(value) < 32 {
		return nil, errors.New("MASTER_KEY must be a base64-encoded 32-byte key or at least 32 characters")
	}
	sum := sha256.Sum256([]byte(value))
	return sum[:], nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback, minValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minValue, maxValue)
	}
	return value, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return value, nil
}

func envBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

func parseOrigins(raw string) map[string]struct{} {
	origins := make(map[string]struct{})
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		parsed, err := url.Parse(candidate)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" && parsed.Path == "" {
			origins[candidate] = struct{}{}
		}
	}
	return origins
}

func parseCIDRs(raw string) ([]*net.IPNet, error) {
	result := make([]*net.IPNet, 0)
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if ip := net.ParseIP(candidate); ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip = ip.To4()
				bits = 32
			}
			result = append(result, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(candidate)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS contains invalid address %q", candidate)
		}
		result = append(result, network)
	}
	return result, nil
}

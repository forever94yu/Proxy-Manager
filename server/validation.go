package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

var (
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	sshUserPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)
	hostnamePattern = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*)$`)
	passwordPattern = regexp.MustCompile(`^[A-Za-z0-9_@%+=,.!?-]{8,128}$`)
)

type FieldErrors map[string]string

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "A JSON request body is required", nil)
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", nil)
		return false
	}
	limited := http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", friendlyJSONError(err), nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "The request body must contain exactly one JSON object", nil)
		return false
	}
	return true
}

func friendlyJSONError(err error) string {
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntaxError):
		return fmt.Sprintf("Malformed JSON near byte %d", syntaxError.Offset)
	case errors.As(err, &typeError):
		if typeError.Field != "" {
			return fmt.Sprintf("Field %s has the wrong type", typeError.Field)
		}
		return "A field has the wrong type"
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return "Unknown field " + strings.TrimPrefix(err.Error(), "json: unknown field ")
	case errors.Is(err, io.EOF):
		return "The request body is empty"
	case strings.Contains(err.Error(), "request body too large"):
		return "The request body exceeds 1 MiB"
	default:
		return "Invalid JSON request body"
	}
}

func validateServerInput(input *ServerInput, credentialRequired bool) FieldErrors {
	fields := make(FieldErrors)
	input.Name = strings.TrimSpace(input.Name)
	input.Host = strings.TrimSpace(input.Host)
	input.SSHUser = strings.TrimSpace(input.SSHUser)
	input.AuthMethod = strings.TrimSpace(input.AuthMethod)
	if input.Name == "" || len([]rune(input.Name)) > 64 {
		fields["name"] = "Name must contain 1 to 64 characters"
	}
	if !validHost(input.Host) {
		fields["host"] = "Host must be a valid IP address or DNS hostname"
	}
	if input.SSHPort < 1 || input.SSHPort > 65535 {
		fields["sshPort"] = "SSH port must be between 1 and 65535"
	}
	if !sshUserPattern.MatchString(input.SSHUser) {
		fields["sshUser"] = "SSH user has an invalid format"
	}
	if input.AuthMethod != "key" && input.AuthMethod != "password" {
		fields["authMethod"] = "Authentication method must be key or password"
	}
	if credentialRequired && input.Credential == "" {
		fields["credential"] = "SSH credential is required"
	}
	if len(input.Credential) > 128*1024 {
		fields["credential"] = "SSH credential is too large"
	}
	if input.AuthMethod == "key" && input.Credential != "" && !strings.Contains(input.Credential, "PRIVATE KEY") {
		fields["credential"] = "Private key must be PEM or OpenSSH private key data"
	}
	if input.HTTPPort < 1 || input.HTTPPort > 65535 {
		fields["httpPort"] = "HTTP port must be between 1 and 65535"
	}
	if input.SocksPort < 1 || input.SocksPort > 65535 {
		fields["socksPort"] = "SOCKS port must be between 1 and 65535"
	}
	if input.HTTPPort == input.SocksPort && input.HTTPPort != 0 {
		fields["socksPort"] = "SOCKS and HTTP ports must be different"
	}
	if len(input.DNS) < 1 || len(input.DNS) > 2 {
		fields["dns"] = "Provide one or two DNS resolver IP addresses"
	} else {
		for _, resolver := range input.DNS {
			ip := net.ParseIP(strings.TrimSpace(resolver))
			if ip == nil || ip.To4() == nil {
				fields["dns"] = "Every DNS resolver must be an IPv4 address"
				break
			}
		}
	}
	input.Tags = sortedUnique(input.Tags)
	if len(input.Tags) > 20 {
		fields["tags"] = "No more than 20 tags are allowed"
	} else {
		for _, tag := range input.Tags {
			if !validTag(tag) {
				fields["tags"] = "Tags must contain 1 to 40 letters, digits, or . _ : / - characters"
				break
			}
		}
	}
	for index := range input.DNS {
		input.DNS[index] = strings.TrimSpace(input.DNS[index])
	}
	return fields
}

func validateUserInput(input *UserInput, creating bool) FieldErrors {
	fields := make(FieldErrors)
	input.Username = strings.TrimSpace(input.Username)
	if !usernamePattern.MatchString(input.Username) {
		fields["username"] = "Username must contain 1 to 64 letters, digits, underscores, or dashes"
	}
	allowedModes := []string{"generated", "custom"}
	if !creating {
		allowedModes = append(allowedModes, "unchanged")
	}
	if !slices.Contains(allowedModes, input.PasswordMode) {
		fields["passwordMode"] = "Password mode is invalid"
	}
	if input.PasswordMode == "custom" {
		if err := validateProxyPassword(input.Password); err != nil {
			fields["password"] = err.Error()
		}
	} else if input.Password != "" {
		fields["password"] = "Password must be omitted unless passwordMode is custom"
	}
	input.ServerIDs = sortedUnique(input.ServerIDs)
	if len(input.ServerIDs) == 0 {
		fields["serverIds"] = "Select at least one server"
	}
	if len(input.ServerIDs) > 500 {
		fields["serverIds"] = "No more than 500 servers may be selected"
	}
	for _, serverID := range input.ServerIDs {
		if !validID(serverID) {
			fields["serverIds"] = "A server identifier is invalid"
			break
		}
	}
	validateUsageInput(input, fields)
	return fields
}

// validateUsageInput checks the optional usage limit fields. Omitted fields are
// valid; applyUsageInput resolves them.
func validateUsageInput(input *UserInput, fields FieldErrors) {
	if input.TrafficLimitBytes != nil && (*input.TrafficLimitBytes < 0 || *input.TrafficLimitBytes > MaxTrafficLimitBytes) {
		fields["trafficLimitBytes"] = "Traffic limit must be between 0 and 1 PiB"
	}
	if input.ExpiresAt != nil {
		trimmed := strings.TrimSpace(*input.ExpiresAt)
		input.ExpiresAt = &trimmed
		if trimmed != "" {
			if _, err := parseUsageTime(trimmed); err != nil {
				fields["expiresAt"] = "Expiry time must be an RFC 3339 timestamp"
			}
		}
	}
	if input.ResetPeriod != nil && !validResetPeriod(*input.ResetPeriod) {
		fields["resetPeriod"] = "Reset period must be none, daily, weekly, or monthly"
	}
	if input.ResetAnchor != nil {
		trimmed := strings.TrimSpace(*input.ResetAnchor)
		input.ResetAnchor = &trimmed
		if trimmed != "" {
			if _, err := parseUsageTime(trimmed); err != nil {
				fields["resetAnchor"] = "Reset anchor must be an RFC 3339 timestamp"
			}
		}
	}
}

// applyUsageInput copies the usage settings of the input onto the user. When
// creating, omitted fields take the defaults (enabled, unlimited, permanent,
// no periodic reset); when editing, they keep their current value. It returns
// field errors that depend on the combination of old and new values.
func applyUsageInput(user *ProxyUser, input UserInput, creating bool, now time.Time) FieldErrors {
	fields := make(FieldErrors)
	previousPeriod, previousAnchor, previousNext := user.ResetPeriod, user.ResetAnchor, user.NextResetAt
	if creating {
		user.Enabled = true
		user.ResetPeriod = ResetNone
	}
	if input.Enabled != nil {
		user.Enabled = *input.Enabled
	}
	if input.TrafficLimitBytes != nil {
		user.TrafficLimitBytes = *input.TrafficLimitBytes
	}
	if input.ExpiresAt != nil {
		user.ExpiresAt = nil
		if *input.ExpiresAt != "" {
			expiresAt, _ := parseUsageTime(*input.ExpiresAt)
			expiresAt = expiresAt.UTC()
			user.ExpiresAt = &expiresAt
		}
	}
	if input.ResetPeriod != nil {
		user.ResetPeriod = *input.ResetPeriod
	}
	if input.ResetAnchor != nil {
		user.ResetAnchor = *input.ResetAnchor
	}
	if user.ResetPeriod == ResetNone {
		user.ResetAnchor = ""
		user.NextResetAt = nil
		return fields
	}
	if user.ResetAnchor == "" {
		fields["resetAnchor"] = "Reset anchor is required for periodic resets"
		return fields
	}
	anchor, err := parseUsageTime(user.ResetAnchor)
	if err != nil {
		fields["resetAnchor"] = "Reset anchor must be an RFC 3339 timestamp"
		return fields
	}
	user.ResetAnchor = formatAnchor(anchor)
	if !creating && previousNext != nil && previousPeriod == user.ResetPeriod && previousAnchor == user.ResetAnchor {
		// Unchanged schedule: keep a reset that may be due but not processed yet.
		return fields
	}
	next := nextResetBoundary(user.ResetPeriod, anchor, now).UTC()
	user.NextResetAt = &next
	return fields
}

func validateProxyPassword(password string) error {
	if !passwordPattern.MatchString(password) {
		return errors.New("Password must contain 8 to 128 letters, digits, or _ @ % + = , . ! ? - characters")
	}
	return nil
}

func validTag(tag string) bool {
	length := 0
	for _, value := range tag {
		length++
		if unicode.IsLetter(value) || unicode.IsDigit(value) || strings.ContainsRune("_.:/-", value) {
			continue
		}
		return false
	}
	return length >= 1 && length <= 40
}

func generateProxyPassword() (string, error) {
	randomBytes := make([]byte, 24)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

func validHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || strings.ContainsAny(host, " /\\\t\r\n") {
		return false
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return true
	}
	return hostnamePattern.MatchString(host)
}

func validID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for index, value := range id {
		switch index {
		case 8, 13, 18, 23:
			if value != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", unicode.ToLower(value)) {
				return false
			}
		}
	}
	return true
}

func validateSearch(value string) error {
	if len(value) > 100 {
		return errors.New("search query is too long")
	}
	return nil
}

package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const sessionCookieName = "proxy_manager_session"

type sessionContextKey struct{}

type sessionClaims struct {
	Subject  string `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

type SessionManager struct {
	secret       []byte
	ttl          time.Duration
	cookieSecure bool
	username     string
	passwordHash [32]byte
}

func NewSessionManager(cfg Config) *SessionManager {
	return &SessionManager{
		secret:       cfg.SessionSecret,
		ttl:          cfg.SessionTTL,
		cookieSecure: cfg.CookieSecure,
		username:     cfg.AdminUsername,
		passwordHash: sha256.Sum256([]byte(cfg.AdminPassword)),
	}
}

func (m *SessionManager) Authenticate(username, password string) bool {
	if subtle.ConstantTimeCompare([]byte(username), []byte(m.username)) != 1 {
		// Perform the password comparison even when the username is wrong.
		candidate := sha256.Sum256([]byte(password))
		subtle.ConstantTimeCompare(candidate[:], m.passwordHash[:])
		return false
	}
	candidate := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(candidate[:], m.passwordHash[:]) == 1
}

func (m *SessionManager) Operator() Operator {
	return Operator{ID: "local-admin", Name: "Administrator", Username: m.username, Role: "admin"}
}

func (m *SessionManager) SetCookie(w http.ResponseWriter, now time.Time) error {
	claims := sessionClaims{
		Subject:  "local-admin",
		Username: m.username,
		Role:     "admin",
		IssuedAt: now.Unix(),
		Expires:  now.Add(m.ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(encoded))
	value := encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(m.ttl.Seconds()),
		Expires:  now.Add(m.ttl),
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (m *SessionManager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (m *SessionManager) Claims(r *http.Request, now time.Time) (sessionClaims, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return sessionClaims{}, errors.New("session cookie is missing")
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return sessionClaims{}, errors.New("invalid session format")
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return sessionClaims{}, errors.New("invalid session signature")
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return sessionClaims{}, errors.New("invalid session signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return sessionClaims{}, errors.New("invalid session payload")
	}
	var claims sessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return sessionClaims{}, errors.New("invalid session payload")
	}
	if claims.Subject != "local-admin" || claims.Username != m.username || claims.Role != "admin" {
		return sessionClaims{}, errors.New("invalid session identity")
	}
	if claims.Expires <= now.Unix() || claims.IssuedAt > now.Add(time.Minute).Unix() {
		return sessionClaims{}, errors.New("session expired")
	}
	return claims, nil
}

func (m *SessionManager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := m.Claims(r, time.Now())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication is required", nil)
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func actorFromContext(ctx context.Context) string {
	if claims, ok := ctx.Value(sessionContextKey{}).(sessionClaims); ok {
		return claims.Username
	}
	return "system"
}

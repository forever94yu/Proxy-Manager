package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type loginAttempt struct {
	failures int
	resetAt  time.Time
}

type loginLimiter struct {
	mu          sync.Mutex
	attempts    map[string]loginAttempt
	limit       int
	window      time.Duration
	lastCleanup time.Time
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{attempts: make(map[string]loginAttempt), limit: limit, window: window}
}

func (l *loginLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	attempt, ok := l.attempts[key]
	if !ok || !now.Before(attempt.resetAt) {
		if ok {
			delete(l.attempts, key)
		}
		return true, 0
	}
	if attempt.failures >= l.limit {
		return false, attempt.resetAt.Sub(now)
	}
	return true, 0
}

func (l *loginLimiter) failure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt, ok := l.attempts[key]
	if !ok || !now.Before(attempt.resetAt) {
		attempt = loginAttempt{resetAt: now.Add(l.window)}
	}
	attempt.failures++
	l.attempts[key] = attempt
}

func (l *loginLimiter) success(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

func (l *loginLimiter) cleanupLocked(now time.Time) {
	if !l.lastCleanup.IsZero() && now.Sub(l.lastCleanup) < time.Minute {
		return
	}
	for key, attempt := range l.attempts {
		if !now.Before(attempt.resetAt) {
			delete(l.attempts, key)
		}
	}
	l.lastCleanup = now
}

func loginClientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	remoteIP := remoteAddressIP(r.RemoteAddr)
	if remoteIP == nil {
		return "unknown"
	}
	if !ipInNetworks(remoteIP, trustedProxies) {
		return remoteIP.String()
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	parsed := make([]net.IP, 0, len(forwarded))
	for _, value := range forwarded {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		ip := net.ParseIP(value)
		if ip == nil {
			return remoteIP.String()
		}
		parsed = append(parsed, ip)
	}
	for index := len(parsed) - 1; index >= 0; index-- {
		if !ipInNetworks(parsed[index], trustedProxies) {
			return parsed[index].String()
		}
	}
	if len(parsed) > 0 {
		return parsed[0].String()
	}
	return remoteIP.String()
}

func remoteAddressIP(address string) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(strings.Trim(strings.TrimSpace(address), "[]"))
}

func ipInNetworks(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

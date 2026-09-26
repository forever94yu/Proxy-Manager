package main

import (
	"strconv"
	"strings"
	"time"
)

const (
	mebibyte = int64(1) << 20
	// MaxTrafficLimitBytes is 1 PiB, the largest quota the console accepts.
	MaxTrafficLimitBytes = int64(1) << 50
	// UnlimitedCapMB mirrors TRAFFIC_UNLIMITED_MB in 3proxy-install.sh: the
	// node-local cap written for accounts without a quota.
	UnlimitedCapMB = MaxTrafficLimitBytes / mebibyte

	UserStatusActive    = "active"
	UserStatusDisabled  = "disabled"
	UserStatusExpired   = "expired"
	UserStatusExhausted = "exhausted"

	NodeStateEnabled  = "enabled"
	NodeStateDisabled = "disabled"
	NodeStateRemoved  = "removed"
)

func validUserStatus(status string) bool {
	switch status {
	case UserStatusActive, UserStatusDisabled, UserStatusExpired, UserStatusExhausted:
		return true
	}
	return false
}

// deriveUserStatus applies the precedence disabled > expired > exhausted > active.
func deriveUserStatus(user ProxyUser, now time.Time) string {
	switch {
	case !user.Enabled:
		return UserStatusDisabled
	case user.ExpiresAt != nil && !now.Before(*user.ExpiresAt):
		return UserStatusExpired
	case user.TrafficLimitBytes > 0 && user.TrafficUsedBytes >= user.TrafficLimitBytes:
		return UserStatusExhausted
	default:
		return UserStatusActive
	}
}

// ObservedPolicy is the last node-reported state of one user on one server.
type ObservedPolicy struct {
	Present      bool
	OnNode       bool
	HasPolicy    bool
	State        string
	CapMB        int64
	Period       int64
	PeriodValid  bool
	CounterBytes int64
	ObservedAt   time.Time
}

// currentCounterBytes is the part of the user's current-period usage that is
// held by the node's live counter, and therefore bounded by the node cap.
func (o ObservedPolicy) currentCounterBytes(periodToken int64) int64 {
	if o.Present && o.HasPolicy && o.PeriodValid && o.Period == periodToken {
		return o.CounterBytes
	}
	return 0
}

// desiredNodePolicy computes the policy a node must enforce for a user. The
// quota is global across servers: each node may let the user consume what is
// left of the quota after the usage recorded everywhere else.
func desiredNodePolicy(user ProxyUser, observed ObservedPolicy, now time.Time) NodePolicy {
	policy := NodePolicy{
		Username: user.Username,
		State:    NodeStateEnabled,
		CapMB:    UnlimitedCapMB,
		Period:   user.PeriodToken,
	}
	if deriveUserStatus(user, now) != UserStatusActive {
		policy.State = NodeStateDisabled
	}
	if user.TrafficLimitBytes > 0 {
		usedElsewhere := user.TrafficUsedBytes - observed.currentCounterBytes(user.PeriodToken)
		policy.CapMB = capMegabytes(user.TrafficLimitBytes - usedElsewhere)
	}
	return policy
}

func capMegabytes(bytes int64) int64 {
	if bytes <= mebibyte {
		return 1
	}
	capMB := (bytes + mebibyte - 1) / mebibyte
	if capMB > UnlimitedCapMB {
		return UnlimitedCapMB
	}
	return capMB
}

// capDriftTolerance keeps multi-server users from reloading every node after
// every traffic collection: node caps are only tightened when they drift by
// more than 5% of the quota (at least 64 MiB). Exhaustion is still detected
// from the global usage on every collection and enforced by disabling.
func capDriftTolerance(limitBytes int64) int64 {
	tolerance := limitBytes / 20
	if tolerance < 64*mebibyte {
		tolerance = 64 * mebibyte
	}
	return tolerance
}

// policyNeedsPush reports whether the node state differs from the desired
// policy enough to schedule a policy-apply.
func policyNeedsPush(user ProxyUser, desired NodePolicy, observed ObservedPolicy) bool {
	if !observed.Present || !observed.OnNode {
		// Accounts missing from a node are the account synchronization job's
		// responsibility, not the policy reconciler's.
		return false
	}
	if !observed.HasPolicy || observed.State != desired.State ||
		!observed.PeriodValid || observed.Period != desired.Period {
		return true
	}
	if observed.CapMB == desired.CapMB {
		return false
	}
	if observed.CapMB == UnlimitedCapMB || desired.CapMB == UnlimitedCapMB {
		return true
	}
	tolerance := capDriftTolerance(user.TrafficLimitBytes)
	observedCap := observed.CapMB * mebibyte
	desiredCap := desired.CapMB * mebibyte
	if desiredCap > observedCap {
		// Raise the node cap only when the node is about to block the user
		// although quota is still available elsewhere.
		return observedCap-observed.CounterBytes < tolerance
	}
	return observedCap-desiredCap > tolerance
}

// parseTrafficReport extracts the last traffic report from installer output.
// Only lines after a `traffic_report v1` marker are considered, and every
// field is validated because the output crosses a trust boundary.
func parseTrafficReport(output string, observedAt time.Time) *TrafficReport {
	var report *TrafficReport
	for _, rawLine := range strings.Split(output, "\n") {
		parts := strings.Split(strings.TrimRight(rawLine, "\r"), "\t")
		if len(parts) < 3 || parts[0] != "PM" {
			continue
		}
		switch parts[1] {
		case "traffic_report":
			if len(parts) == 3 && parts[2] == "v1" {
				report = &TrafficReport{ObservedAt: observedAt, NodeUsers: []string{}, Entries: []TrafficEntry{}}
			} else {
				report = nil
			}
		case "node_user":
			if report != nil && len(parts) == 3 && usernamePattern.MatchString(parts[2]) {
				report.NodeUsers = append(report.NodeUsers, parts[2])
			}
		case "traffic":
			if report == nil || len(parts) != 8 {
				continue
			}
			if entry, ok := parseTrafficEntry(parts[2:]); ok {
				report.Entries = append(report.Entries, entry)
			}
		}
	}
	return report
}

func parseTrafficEntry(fields []string) (TrafficEntry, bool) {
	name, state := fields[0], fields[1]
	if !usernamePattern.MatchString(name) {
		return TrafficEntry{}, false
	}
	if state != NodeStateEnabled && state != NodeStateDisabled && state != NodeStateRemoved {
		return TrafficEntry{}, false
	}
	capMB, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || capMB < 1 || capMB > UnlimitedCapMB {
		return TrafficEntry{}, false
	}
	period, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || period < 0 {
		return TrafficEntry{}, false
	}
	index, err := strconv.Atoi(fields[4])
	if err != nil || index < 1 || index > 9_999_999 {
		return TrafficEntry{}, false
	}
	bytes, err := strconv.ParseUint(fields[5], 10, 64)
	if err != nil {
		return TrafficEntry{}, false
	}
	if bytes > uint64(MaxTrafficLimitBytes)*1024 {
		bytes = uint64(MaxTrafficLimitBytes) * 1024
	}
	return TrafficEntry{Username: name, State: state, CapMB: capMB, Period: period, Index: index, Bytes: int64(bytes)}, true
}

// isPolicyJobType reports job types whose targets run policy-apply and resolve
// the desired state at execution time.
func isPolicyJobType(jobType string) bool {
	switch jobType {
	case "user_policy", "user_traffic_reset", "user_enable", "user_disable":
		return true
	}
	return false
}

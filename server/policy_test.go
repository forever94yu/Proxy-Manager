package main

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveUserStatusPrecedence(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	cases := []struct {
		name string
		user ProxyUser
		want string
	}{
		{"active unlimited", ProxyUser{Enabled: true}, UserStatusActive},
		{"active below quota", ProxyUser{Enabled: true, TrafficLimitBytes: 100, TrafficUsedBytes: 99, ExpiresAt: &future}, UserStatusActive},
		{"exhausted", ProxyUser{Enabled: true, TrafficLimitBytes: 100, TrafficUsedBytes: 100}, UserStatusExhausted},
		{"expired beats exhausted", ProxyUser{Enabled: true, TrafficLimitBytes: 100, TrafficUsedBytes: 200, ExpiresAt: &past}, UserStatusExpired},
		{"expiry instant is expired", ProxyUser{Enabled: true, ExpiresAt: &now}, UserStatusExpired},
		{"disabled beats all", ProxyUser{Enabled: false, TrafficLimitBytes: 1, TrafficUsedBytes: 2, ExpiresAt: &past}, UserStatusDisabled},
	}
	for _, testCase := range cases {
		if got := deriveUserStatus(testCase.user, now); got != testCase.want {
			t.Errorf("%s: status = %s, want %s", testCase.name, got, testCase.want)
		}
	}
}

func TestDesiredNodePolicySplitsGlobalQuota(t *testing.T) {
	now := time.Now()
	user := ProxyUser{Username: "alice", Enabled: true, TrafficLimitBytes: 1000 * mebibyte, TrafficUsedBytes: 300 * mebibyte, PeriodToken: 4}
	// 100 MiB of the usage is on this node's live counter, 200 MiB elsewhere.
	observed := ObservedPolicy{Present: true, OnNode: true, HasPolicy: true, Period: 4, PeriodValid: true, CounterBytes: 100 * mebibyte}
	policy := desiredNodePolicy(user, observed, now)
	if policy.State != NodeStateEnabled || policy.Period != 4 || policy.CapMB != 800 || policy.Username != "alice" {
		t.Fatalf("unexpected policy: %+v", policy)
	}

	// A counter of an older period does not count towards the local share.
	observed.Period = 3
	if policy := desiredNodePolicy(user, observed, now); policy.CapMB != 700 {
		t.Fatalf("cap with stale counter = %d, want 700", policy.CapMB)
	}

	unlimited := desiredNodePolicy(ProxyUser{Username: "bob", Enabled: true}, ObservedPolicy{}, now)
	if unlimited.CapMB != UnlimitedCapMB || unlimited.State != NodeStateEnabled {
		t.Fatalf("unexpected unlimited policy: %+v", unlimited)
	}

	exhausted := user
	exhausted.TrafficUsedBytes = 2000 * mebibyte
	if policy := desiredNodePolicy(exhausted, ObservedPolicy{}, now); policy.State != NodeStateDisabled || policy.CapMB != 1 {
		t.Fatalf("unexpected exhausted policy: %+v", policy)
	}
}

func TestCapMegabytesRoundsUpAndClamps(t *testing.T) {
	cases := map[int64]int64{
		-5:                       1,
		0:                        1,
		mebibyte:                 1,
		mebibyte + 1:             2,
		10 * mebibyte:            10,
		MaxTrafficLimitBytes * 2: UnlimitedCapMB,
	}
	for bytes, want := range cases {
		if got := capMegabytes(bytes); got != want {
			t.Errorf("capMegabytes(%d) = %d, want %d", bytes, got, want)
		}
	}
}

func TestPolicyNeedsPush(t *testing.T) {
	user := ProxyUser{Username: "alice", Enabled: true, TrafficLimitBytes: 10240 * mebibyte, PeriodToken: 1}
	desired := NodePolicy{Username: "alice", State: NodeStateEnabled, CapMB: 5000, Period: 1}
	base := ObservedPolicy{Present: true, OnNode: true, HasPolicy: true, State: NodeStateEnabled, CapMB: 5000, Period: 1, PeriodValid: true}
	cases := []struct {
		name   string
		mutate func(*ObservedPolicy)
		want   bool
	}{
		{"in sync", func(*ObservedPolicy) {}, false},
		{"never observed", func(o *ObservedPolicy) { *o = ObservedPolicy{} }, false},
		{"account missing on node", func(o *ObservedPolicy) { o.OnNode = false }, false},
		{"legacy account without policy", func(o *ObservedPolicy) { o.HasPolicy = false }, true},
		{"state differs", func(o *ObservedPolicy) { o.State = NodeStateDisabled }, true},
		{"period differs", func(o *ObservedPolicy) { o.Period = 0 }, true},
		{"small cap drift", func(o *ObservedPolicy) { o.CapMB = 5100 }, false},
		{"large cap drift", func(o *ObservedPolicy) { o.CapMB = 6000 }, true},
		{"node cap lower, far from blocking", func(o *ObservedPolicy) { o.CapMB = 4000; o.CounterBytes = 100 * mebibyte }, false},
		{"node cap lower, about to block", func(o *ObservedPolicy) { o.CapMB = 4000; o.CounterBytes = 3990 * mebibyte }, true},
		{"unlimited on node", func(o *ObservedPolicy) { o.CapMB = UnlimitedCapMB }, true},
	}
	for _, testCase := range cases {
		observed := base
		testCase.mutate(&observed)
		if got := policyNeedsPush(user, desired, observed); got != testCase.want {
			t.Errorf("%s: needsPush = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestParseTrafficReport(t *testing.T) {
	observedAt := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	output := strings.Join([]string{
		"PM\tresult\tcreated",
		"PM\tuser\tbob",
		"PM\ttraffic\tstale\tenabled\t10\t0\t1\t5", // before any marker: ignored
		"PM\ttraffic_report\tv1",
		"PM\tnode_user\talice",
		"PM\tnode_user\tbad name",
		"PM\ttraffic\tbob\tremoved\t100\t2\t3\t4096",
		"PM\ttraffic\talice\tenabled\t1073741824\t0\t1\t123456789",
		"PM\ttraffic\talice\tpaused\t10\t0\t1\t5",
		"PM\ttraffic\tcarol\tdisabled\t0\t0\t2\t5",
		"PM\ttraffic\tdave\tdisabled\t10\t-1\t2\t5",
		"PM\ttraffic\terin\tdisabled\t10\t1\t2\t-5",
		"PM\ttraffic\tfrank\tdisabled\t10\t1\t2",
		"",
	}, "\n")
	report := parseTrafficReport(output, observedAt)
	if report == nil {
		t.Fatal("report was not parsed")
	}
	if len(report.NodeUsers) != 1 || report.NodeUsers[0] != "alice" {
		t.Fatalf("node users = %v", report.NodeUsers)
	}
	if len(report.Entries) != 2 {
		t.Fatalf("entries = %+v", report.Entries)
	}
	if entry := report.Entries[0]; entry.Username != "bob" || entry.State != NodeStateRemoved || entry.Bytes != 4096 || entry.Period != 2 || entry.Index != 3 {
		t.Fatalf("unexpected removed entry: %+v", entry)
	}
	if entry := report.Entries[1]; entry.Username != "alice" || entry.CapMB != UnlimitedCapMB || entry.Bytes != 123456789 {
		t.Fatalf("unexpected live entry: %+v", entry)
	}
	if !report.ObservedAt.Equal(observedAt) {
		t.Fatalf("observedAt = %s", report.ObservedAt)
	}

	if parseTrafficReport("PM\tresult\tdeleted\n", observedAt) != nil {
		t.Fatal("output without a marker produced a report")
	}
	// The last report wins.
	twice := "PM\ttraffic_report\tv1\nPM\tnode_user\tx\nPM\ttraffic_report\tv1\nPM\tnode_user\ty\n"
	if report := parseTrafficReport(twice, observedAt); len(report.NodeUsers) != 1 || report.NodeUsers[0] != "y" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestTrafficRowCounterTransitions(t *testing.T) {
	var row trafficRow
	row.applyLive(TrafficEntry{State: NodeStateEnabled, CapMB: 10, Period: 1, Index: 1, Bytes: 100})
	row.applyLive(TrafficEntry{State: NodeStateEnabled, CapMB: 10, Period: 1, Index: 1, Bytes: 150})
	if row.CounterBytes != 150 || row.RetainedBytes != 0 {
		t.Fatalf("growth: %+v", row)
	}
	// Account removed on the node with a final value; the report is repeated.
	row.applyRemoved(TrafficEntry{State: NodeStateRemoved, Period: 1, Index: 1, Bytes: 170})
	row.applyRemoved(TrafficEntry{State: NodeStateRemoved, Period: 1, Index: 1, Bytes: 170})
	row.applyAbsent(false)
	if row.RetainedBytes != 0 || row.CounterBytes != 170 || row.OnNode || row.State != NodeStateRemoved {
		t.Fatalf("removed: %+v", row)
	}
	// Re-created on a reused slot whose first observation is already larger:
	// still a new counter because the old one was final.
	row.applyLive(TrafficEntry{State: NodeStateEnabled, CapMB: 10, Period: 1, Index: 1, Bytes: 400})
	if row.RetainedBytes != 170 || row.CounterBytes != 400 || row.CounterIndex != 1 {
		t.Fatalf("re-created: %+v", row)
	}
	// Same index but a lower value: a fresh counter on a reused slot.
	row.applyLive(TrafficEntry{State: NodeStateEnabled, CapMB: 10, Period: 1, Index: 1, Bytes: 5})
	if row.RetainedBytes != 570 || row.CounterBytes != 5 {
		t.Fatalf("reused slot: %+v", row)
	}
	// A new period starts from zero.
	row.applyLive(TrafficEntry{State: NodeStateEnabled, CapMB: 10, Period: 2, Index: 5, Bytes: 7})
	if row.RetainedBytes != 0 || row.CounterBytes != 7 || row.Period.Int64 != 2 {
		t.Fatalf("new period: %+v", row)
	}
}

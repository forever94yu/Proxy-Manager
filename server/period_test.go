package main

import (
	"testing"
	"time"
)

func mustUsageTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := parseUsageTime(value)
	if err != nil {
		t.Fatalf("parseUsageTime(%q): %v", value, err)
	}
	return parsed
}

func TestNextResetBoundaryMonthlyClampsWithoutDrift(t *testing.T) {
	anchor := mustUsageTime(t, "2026-01-31T10:00:00+08:00")
	cases := []struct {
		after string
		want  string
	}{
		{"2026-01-31T09:59:59+08:00", "2026-01-31T10:00:00+08:00"},
		{"2026-01-31T10:00:00+08:00", "2026-02-28T10:00:00+08:00"},
		{"2026-02-28T10:00:00+08:00", "2026-03-31T10:00:00+08:00"},
		{"2026-04-01T00:00:00+08:00", "2026-04-30T10:00:00+08:00"},
		{"2028-02-01T00:00:00+08:00", "2028-02-29T10:00:00+08:00"},
		{"2026-12-31T10:00:00+08:00", "2027-01-31T10:00:00+08:00"},
		// Anchor in the future: the schedule extends backwards from it.
		{"2025-11-15T00:00:00+08:00", "2025-11-30T10:00:00+08:00"},
	}
	for _, testCase := range cases {
		got := nextResetBoundary(ResetMonthly, anchor, mustUsageTime(t, testCase.after))
		if want := mustUsageTime(t, testCase.want); !got.Equal(want) {
			t.Errorf("next after %s = %s, want %s", testCase.after, got.Format(time.RFC3339), testCase.want)
		}
	}
}

func TestNextResetBoundaryDailyAndWeekly(t *testing.T) {
	anchor := mustUsageTime(t, "2026-03-02T00:30:00+08:00")
	after := mustUsageTime(t, "2026-09-26T12:00:00Z") // 20:00 +08:00
	if got, want := nextResetBoundary(ResetDaily, anchor, after), mustUsageTime(t, "2026-09-27T00:30:00+08:00"); !got.Equal(want) {
		t.Errorf("daily = %s, want %s", got, want)
	}
	// 2026-03-02 is a Monday; so is 2026-09-28.
	if got, want := nextResetBoundary(ResetWeekly, anchor, after), mustUsageTime(t, "2026-09-28T00:30:00+08:00"); !got.Equal(want) {
		t.Errorf("weekly = %s, want %s", got, want)
	}
	if got, want := currentPeriodStart(ResetWeekly, anchor, after), mustUsageTime(t, "2026-09-21T00:30:00+08:00"); !got.Equal(want) {
		t.Errorf("weekly period start = %s, want %s", got, want)
	}
}

func TestCurrentPeriodStartCatchesUpMissedBoundaries(t *testing.T) {
	anchor := mustUsageTime(t, "2026-01-15T00:00:00Z")
	now := mustUsageTime(t, "2026-06-20T08:00:00Z")
	if got, want := currentPeriodStart(ResetMonthly, anchor, now), mustUsageTime(t, "2026-06-15T00:00:00Z"); !got.Equal(want) {
		t.Errorf("period start = %s, want %s", got, want)
	}
	// A boundary instant belongs to the period it starts.
	boundary := mustUsageTime(t, "2026-07-15T00:00:00Z")
	if got := currentPeriodStart(ResetMonthly, anchor, boundary); !got.Equal(boundary) {
		t.Errorf("period start at boundary = %s, want %s", got, boundary)
	}
}

func TestParseUsageTimeKeepsOffsetAndRejectsGarbage(t *testing.T) {
	parsed := mustUsageTime(t, "2026-05-01T09:30:00-04:00")
	if _, offset := parsed.Zone(); offset != -4*3600 {
		t.Fatalf("offset = %d, want -14400", offset)
	}
	if formatAnchor(parsed) != "2026-05-01T09:30:00-04:00" {
		t.Fatalf("formatAnchor = %s", formatAnchor(parsed))
	}
	for _, value := range []string{"", "2026-05-01", "tomorrow", "0001-01-01T00:00:00Z"} {
		if _, err := parseUsageTime(value); err == nil {
			t.Errorf("parseUsageTime(%q) succeeded", value)
		}
	}
}

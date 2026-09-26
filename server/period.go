package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ResetNone    = "none"
	ResetDaily   = "daily"
	ResetWeekly  = "weekly"
	ResetMonthly = "monthly"
)

func validResetPeriod(period string) bool {
	switch period {
	case ResetNone, ResetDaily, ResetWeekly, ResetMonthly:
		return true
	}
	return false
}

// parseUsageTime parses an RFC 3339 timestamp and pins it to a fixed UTC offset
// equal to the one it was written with. Reset schedules are evaluated in that
// fixed offset, so "every month on the 15th at 10:00 +08:00" does not move
// with the control plane's own time zone or its daylight saving rules.
func parseUsageTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	if parsed.Year() < 2000 || parsed.Year() > 9999 {
		return time.Time{}, fmt.Errorf("timestamp year %d is out of range", parsed.Year())
	}
	_, offset := parsed.Zone()
	return parsed.In(time.FixedZone("", offset)).Truncate(time.Second), nil
}

// formatAnchor stores an anchor with its UTC offset so the schedule can be
// reconstructed exactly.
func formatAnchor(anchor time.Time) string {
	return anchor.Format(time.RFC3339)
}

// periodBoundary returns anchor + k periods. Monthly boundaries are always
// derived from the anchor itself, so a 31st anchor yields Jan 31, Feb 28/29,
// Mar 31, … and never drifts to the 28th.
func periodBoundary(period string, anchor time.Time, k int) time.Time {
	switch period {
	case ResetDaily:
		return anchor.AddDate(0, 0, k)
	case ResetWeekly:
		return anchor.AddDate(0, 0, 7*k)
	case ResetMonthly:
		year, month, day := anchor.Date()
		total := int(month) - 1 + k
		year += floorDiv(total, 12)
		targetMonth := time.Month(total - floorDiv(total, 12)*12 + 1)
		if last := daysInMonth(year, targetMonth); day > last {
			day = last
		}
		hour, minute, second := anchor.Clock()
		return time.Date(year, targetMonth, day, hour, minute, second, anchor.Nanosecond(), anchor.Location())
	}
	return anchor
}

// nextResetBoundary returns the first boundary of the schedule that is strictly
// after `after`. The schedule extends in both directions from the anchor, so
// an anchor in the future only sets the phase of the cycle.
func nextResetBoundary(period string, anchor, after time.Time) time.Time {
	return periodBoundary(period, anchor, boundaryIndexAfter(period, anchor, after))
}

// currentPeriodStart returns the latest boundary at or before `at`.
func currentPeriodStart(period string, anchor, at time.Time) time.Time {
	return periodBoundary(period, anchor, boundaryIndexAfter(period, anchor, at)-1)
}

// boundaryIndexAfter returns the smallest k whose boundary is after `after`.
// Boundaries are strictly increasing in k, so boundary k-1 is at or before it.
func boundaryIndexAfter(period string, anchor, after time.Time) int {
	k := estimatePeriods(period, anchor, after)
	for !periodBoundary(period, anchor, k).After(after) {
		k++
	}
	for periodBoundary(period, anchor, k-1).After(after) {
		k--
	}
	return k
}

func estimatePeriods(period string, anchor, at time.Time) int {
	switch period {
	case ResetDaily:
		return int(at.Sub(anchor) / (24 * time.Hour))
	case ResetWeekly:
		return int(at.Sub(anchor) / (7 * 24 * time.Hour))
	case ResetMonthly:
		local := at.In(anchor.Location())
		return (local.Year()-anchor.Year())*12 + int(local.Month()) - int(anchor.Month())
	}
	return 0
}

func floorDiv(value, divisor int) int {
	quotient := value / divisor
	if value%divisor != 0 && (value < 0) != (divisor < 0) {
		quotient--
	}
	return quotient
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"testing"
	"time"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("bad date %q: %v", s, err)
	}
	return ts
}

// weeklySeries builds a weekly series anchored on Friday 2025-10-10 07:30Z
// with a Thursday weekday mask (the mask, not the anchor weekday, drives the
// series once it starts).
func weeklySeries(t *testing.T, until time.Time) EventItem {
	t.Helper()
	return EventItem{
		ServerID:  "23:24",
		Subject:   "Еженедельный статус (производство)",
		StartTime: mustDate(t, "2025-10-10").Add(7*time.Hour + 30*time.Minute),
		EndTime:   mustDate(t, "2025-10-10").Add(9 * time.Hour),
		Recurrence: &Recurrence{
			Type:      RecurrenceWeekly,
			Interval:  1,
			DayOfWeek: DowThursday,
			Until:     until,
		},
	}
}

func TestExpandRecurrences_weeklyInWindow(t *testing.T) {
	ev := weeklySeries(t, time.Time{}) // infinite
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-20"))
	// Thursdays 12 and 19 Nov 2026 fall in [11, 20); 11 Nov is a Wednesday.
	want := []string{"2026-11-12", "2026-11-19"}
	if len(got) != len(want) {
		t.Fatalf("got %d occurrences, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if d := got[i].StartTime.Format("2006-01-02"); d != w {
			t.Errorf("occurrence %d start = %s, want %s", i, d, w)
		}
		if !got[i].StartTime.Equal(mustDate(t, w).Add(7*time.Hour + 30*time.Minute)) {
			t.Errorf("occurrence %d time-of-day drifted: %v", i, got[i].StartTime)
		}
		if got[i].ServerID != "23:24" || got[i].Subject != ev.Subject {
			t.Errorf("occurrence %d lost item fields: %+v", i, got[i])
		}
	}
}

func TestExpandRecurrences_untilStopsSeries(t *testing.T) {
	ev := weeklySeries(t, mustDate(t, "2026-11-12")) // ends on the 12th
	// Nov 5 is also a Thursday and lies before Until, but it is outside the
	// window; Nov 19 would be past Until and must not appear.
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-12-31"))
	if len(got) != 1 || got[0].StartTime.Format("2006-01-02") != "2026-11-12" {
		t.Fatalf("want exactly the 12th, got %+v", got)
	}
}

func TestExpandRecurrences_occurrencesLimit(t *testing.T) {
	ev := EventItem{
		ServerID:  "f:1",
		Subject:   "daily",
		StartTime: mustDate(t, "2026-11-01").Add(9 * time.Hour),
		EndTime:   mustDate(t, "2026-11-01").Add(10 * time.Hour),
		Recurrence: &Recurrence{
			Type:        RecurrenceDaily,
			Interval:    1,
			Occurrences: 3, // 1st, 2nd, 3rd Nov only
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-01").Add(-30*24*time.Hour), mustDate(t, "2026-12-31"))
	if len(got) != 3 {
		t.Fatalf("want 3 occurrences, got %d", len(got))
	}
	if got[2].StartTime.Format("2006-01-02") != "2026-11-03" {
		t.Errorf("last occurrence = %s, want 2026-11-03", got[2].StartTime.Format("2006-01-02"))
	}
}

func TestExpandRecurrences_deletedAndModifiedExceptions(t *testing.T) {
	ev := weeklySeries(t, time.Time{})
	ev.Exceptions = []Exception{
		{ExceptionStartTime: mustDate(t, "2026-11-12").Add(7*time.Hour + 30*time.Minute), Deleted: true},
		{
			ExceptionStartTime: mustDate(t, "2026-11-19").Add(7*time.Hour + 30*time.Minute),
			Subject:            "Статус: перенос",
			Location:           "Zoom (новое)",
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-20"))
	if len(got) != 1 {
		t.Fatalf("want 1 occurrence (12th deleted), got %d", len(got))
	}
	if got[0].Subject != "Статус: перенос" || got[0].Location != "Zoom (новое)" {
		t.Errorf("modified exception not applied: %+v", got[0])
	}
}

func TestExpandRecurrences_modifiedTimeOnlyWhenBothSides(t *testing.T) {
	ev := weeklySeries(t, time.Time{})
	base := mustDate(t, "2026-11-12").Add(7*time.Hour + 30*time.Minute)
	// Only a new Start given → keep the computed window.
	ev.Exceptions = []Exception{{ExceptionStartTime: base, StartTime: base.Add(-time.Hour)}}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-13"))
	if len(got) != 1 || !got[0].StartTime.Equal(base) {
		t.Fatalf("single-sided time override must be ignored, got %+v", got)
	}
	// Both sides → applied.
	ev.Exceptions = []Exception{{ExceptionStartTime: base, StartTime: base.Add(-time.Hour), EndTime: base.Add(-time.Hour + 90*time.Minute)}}
	got = ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-13"))
	if len(got) != 1 || !got[0].StartTime.Equal(base.Add(-time.Hour)) || !got[0].EndTime.Equal(base.Add(-time.Hour+90*time.Minute)) {
		t.Fatalf("both-sided time override not applied, got %+v", got)
	}
}

func TestExpandRecurrences_monthlyDateSkipsShortMonths(t *testing.T) {
	// Every 31st: Jan 31, Mar 31, May 31, Jul 31, Aug 31, Oct 31, Dec 31 —
	// Feb/Apr/Jun/Sep/Nov have no 31st and are skipped, not clamped.
	ev := EventItem{
		ServerID:  "f:2",
		StartTime: mustDate(t, "2026-01-31").Add(10 * time.Hour),
		EndTime:   mustDate(t, "2026-01-31").Add(11 * time.Hour),
		Recurrence: &Recurrence{
			Type:       RecurrenceMonthlyDate,
			Interval:   1,
			DayOfMonth: 31,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-01-01"), mustDate(t, "2027-01-01"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	want := []string{"2026-01-31", "2026-03-31", "2026-05-31", "2026-07-31", "2026-08-31", "2026-10-31", "2026-12-31"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
	}
}

func TestExpandRecurrences_monthlyByDayLast(t *testing.T) {
	// Last Friday of each month.
	ev := EventItem{
		ServerID:  "f:3",
		StartTime: mustDate(t, "2026-11-27").Add(15 * time.Hour), // Fri
		EndTime:   mustDate(t, "2026-11-27").Add(16 * time.Hour),
		Recurrence: &Recurrence{
			Type:        RecurrenceMonthlyByDay,
			Interval:    1,
			DayOfWeek:   DowFriday,
			WeekOfMonth: 5,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2027-03-01"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	// 2026: Nov 27, Dec 25; 2027: Jan 29, Feb 26.
	want := []string{"2026-11-27", "2026-12-25", "2027-01-29", "2027-02-26"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
		// The anchor's 15:00 clock time must survive on every occurrence.
		if !got[i].StartTime.Equal(mustDate(t, want[i]).Add(15 * time.Hour)) {
			t.Errorf("occurrence %d time-of-day drifted: %v", i, got[i].StartTime)
		}
		if !got[i].EndTime.Equal(mustDate(t, want[i]).Add(16 * time.Hour)) {
			t.Errorf("occurrence %d end drifted: %v", i, got[i].EndTime)
		}
	}
}

func TestExpandRecurrences_yearlyByDayLast(t *testing.T) {
	// Last Friday of June every year, anchored Fri 2026-06-26 14:00Z.
	ev := EventItem{
		ServerID:  "f:6",
		StartTime: mustDate(t, "2026-06-26").Add(14 * time.Hour),
		EndTime:   mustDate(t, "2026-06-26").Add(15 * time.Hour),
		Recurrence: &Recurrence{
			Type:        RecurrenceYearlyByDay,
			Interval:    1,
			MonthOfYear: 6,
			DayOfWeek:   DowFriday,
			WeekOfMonth: 5,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-01-01"), mustDate(t, "2029-01-01"))
	// 2026: Jun 26; 2027: Jun 25; 2028: Jun 30.
	want := []string{"2026-06-26", "2027-06-25", "2028-06-30"}
	if len(got) != len(want) {
		t.Fatalf("got %d occurrences, want %d", len(got), len(want))
	}
	for i, w := range want {
		if d := got[i].StartTime.Format("2006-01-02"); d != w {
			t.Errorf("date %d = %s, want %s", i, d, w)
		}
		// Full-timestamp comparison: the anchor's 14:00/15:00 window must
		// survive on every occurrence, not just the anchor itself.
		if !got[i].StartTime.Equal(mustDate(t, w).Add(14 * time.Hour)) {
			t.Errorf("occurrence %d time-of-day drifted: %v", i, got[i].StartTime)
		}
		if !got[i].EndTime.Equal(mustDate(t, w).Add(15 * time.Hour)) {
			t.Errorf("occurrence %d end drifted: %v", i, got[i].EndTime)
		}
	}
}

func TestExpandRecurrences_yearlyFeb29SkippedNonLeap(t *testing.T) {
	ev := EventItem{
		ServerID:  "f:4",
		StartTime: mustDate(t, "2024-02-29").Add(8 * time.Hour),
		EndTime:   mustDate(t, "2024-02-29").Add(9 * time.Hour),
		Recurrence: &Recurrence{
			Type:        RecurrenceYearlyDate,
			Interval:    1,
			MonthOfYear: 2,
			DayOfMonth:  29,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2024-01-01"), mustDate(t, "2033-01-01"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	// Leap years only: 2024, 2028, 2032.
	want := []string{"2024-02-29", "2028-02-29", "2032-02-29"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
	}
}

func TestExpandRecurrences_multiDayWeeklyMask(t *testing.T) {
	// Mon+Thu weekly, anchored Monday 2026-11-09.
	ev := EventItem{
		ServerID:  "f:5",
		StartTime: mustDate(t, "2026-11-09").Add(10 * time.Hour),
		EndTime:   mustDate(t, "2026-11-09").Add(11 * time.Hour),
		Recurrence: &Recurrence{
			Type:      RecurrenceWeekly,
			Interval:  1,
			DayOfWeek: DowMonday | DowThursday,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-09"), mustDate(t, "2026-11-23"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	want := []string{"2026-11-09", "2026-11-12", "2026-11-16", "2026-11-19"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
	}
}

func TestExpandRecurrences_anchorOutsideWindowNotShown(t *testing.T) {
	ev := weeklySeries(t, time.Time{}) // anchored 2025-10-10
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-18"))
	for _, g := range got {
		if g.StartTime.Before(mustDate(t, "2026-11-11")) {
			t.Fatalf("anchor leaked into results: %+v", g)
		}
	}
}

func TestExpandRecurrences_anchorAfterWindowEmpty(t *testing.T) {
	// Anchor Fri 2026-12-04 lies strictly after windowEnd → early break,
	// zero results (the series simply has not started inside the window).
	ev := EventItem{
		ServerID:  "f:7",
		StartTime: mustDate(t, "2026-12-04").Add(9 * time.Hour),
		EndTime:   mustDate(t, "2026-12-04").Add(10 * time.Hour),
		Recurrence: &Recurrence{
			Type:     RecurrenceDaily,
			Interval: 1,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2026-11-08"))
	if len(got) != 0 {
		t.Fatalf("anchor after window must yield nothing, got %+v", got)
	}
}

func TestExpandRecurrences_biweeklyGridPhase(t *testing.T) {
	// Every-2-weeks Thursday series anchored (first instance) Thu
	// 2025-10-09: grid weeks alternate, so in late 2026 the Thursdays
	// Nov 5, Nov 19, Dec 3 occur while Nov 12 and Nov 26 are skipped.
	ev := EventItem{
		ServerID:  "f:8",
		StartTime: mustDate(t, "2025-10-09").Add(7*time.Hour + 30*time.Minute),
		EndTime:   mustDate(t, "2025-10-09").Add(9 * time.Hour),
		Recurrence: &Recurrence{
			Type:      RecurrenceWeekly,
			Interval:  2,
			DayOfWeek: DowThursday,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2026-12-08"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	want := []string{"2026-11-05", "2026-11-19", "2026-12-03"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
	}
}

func TestExpandRecurrences_deletedConsumesOccurrencesBudget(t *testing.T) {
	// Budget of 3 daily instances: Nov 1, 2, 3. The Nov 2 instance is
	// deleted, but it still consumed its slot, so exactly two rows
	// remain and no Nov 4 appears.
	ev := EventItem{
		ServerID:  "f:9",
		StartTime: mustDate(t, "2026-11-01").Add(9 * time.Hour),
		EndTime:   mustDate(t, "2026-11-01").Add(10 * time.Hour),
		Recurrence: &Recurrence{
			Type:        RecurrenceDaily,
			Interval:    1,
			Occurrences: 3,
		},
		Exceptions: []Exception{
			{ExceptionStartTime: mustDate(t, "2026-11-02").Add(9 * time.Hour), Deleted: true},
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2026-11-30"))
	if len(got) != 2 {
		t.Fatalf("want 2 occurrences (deleted one consumed its slot), got %d", len(got))
	}
	if got[1].StartTime.Format("2006-01-02") != "2026-11-03" {
		t.Errorf("last occurrence = %s, want 2026-11-03", got[1].StartTime.Format("2006-01-02"))
	}
}

func TestExpandRecurrences_malformedRuleYieldsNothing(t *testing.T) {
	cases := []EventItem{
		{ServerID: "a", StartTime: mustDate(t, "2026-11-01"), Recurrence: &Recurrence{Type: RecurrenceDaily}}, // zero interval
		{ServerID: "b", StartTime: mustDate(t, "2026-11-01"), Recurrence: &Recurrence{Type: 99, Interval: 1}}, // unknown type
	}
	for _, ev := range cases {
		if got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2026-12-01")); len(got) != 0 {
			t.Errorf("%s: malformed rule produced %d occurrences, want 0", ev.ServerID, len(got))
		}
	}
}

func TestExpandRecurrences_nonRecurringReturnsNil(t *testing.T) {
	ev := EventItem{ServerID: "x", StartTime: mustDate(t, "2026-11-12")}
	if got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2026-12-01")); got != nil {
		t.Fatalf("non-recurring item must yield nil, got %+v", got)
	}
}

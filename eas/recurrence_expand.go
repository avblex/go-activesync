// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"time"
)

// maxOccurrenceIterations caps the walk from the anchor, guarding against
// malformed rules whose step never advances. Normal series stop at the
// window edge or their Until/Occurrences long before this. The cap bounds the
// walk, not the output: a series anchored far enough in the past that
// reaching windowStart would take more steps than the ceiling (e.g. a daily
// series older than ~27 years) may under-report its occurrences.
const maxOccurrenceIterations = 10000

// weekEpoch is a known-Sunday midnight used to bucket calendar dates into
// absolute week numbers for the weekly rule. (1970-01-04 was a Sunday.)
var weekEpoch = time.Date(1970, 1, 4, 0, 0, 0, 0, time.UTC)

// weekIndex returns the absolute week number containing d (date part only,
// UTC). Two dates share a week iff their indices are equal.
func weekIndex(d time.Time) int {
	dd := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	return int(dd.Sub(weekEpoch).Hours() / 168)
}

// timeOfDay returns t's offset from midnight UTC of its own date — the
// anchor's clock time, which every occurrence of a series must carry. Callers
// normalize to UTC first, so this equals the wall-clock time.
func timeOfDay(t time.Time) time.Duration {
	return t.Sub(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC))
}

// ExpandRecurrences returns one EventItem per occurrence of ev's series that
// starts within [windowStart, windowEnd), with exceptions applied. For a
// non-recurring item it returns nil (callers keep the original row).
//
// Occurrences are generated forward from the anchor (ev.StartTime); the
// anchor itself is included only if it lies inside the window. Time-of-day is
// fixed from the anchor; only the date part advances. Everything is computed
// in absolute (UTC) time — no local-zone or DST conversion.
//
// The function never errors: a malformed rule simply yields no occurrences.
func ExpandRecurrences(ev EventItem, windowStart, windowEnd time.Time) []EventItem {
	if ev.Recurrence == nil {
		return nil
	}
	r := ev.Recurrence
	if r.Interval <= 0 || !ruleSatisfiable(r) {
		return nil
	}

	// Normalize to UTC up front: parseEASTime accepts offset-bearing
	// RFC3339, and both the exception map keys and the time-of-day math
	// below assume UTC wall clock equals absolute time.
	ev.StartTime = ev.StartTime.UTC()
	ev.EndTime = ev.EndTime.UTC()
	windowStart = windowStart.UTC()
	windowEnd = windowEnd.UTC()

	exc := map[time.Time]Exception{}
	for _, x := range ev.Exceptions {
		if !x.ExceptionStartTime.IsZero() {
			exc[x.ExceptionStartTime.UTC()] = x
		}
	}

	var out []EventItem
	anchor := ev.StartTime
	cur := anchor
	count := 0
	for count < maxOccurrenceIterations {
		count++
		if !cur.Before(windowEnd) {
			break
		}
		if !cur.Before(windowStart) {
			item := ev
			// The occurrence carries its own computed window: the anchor's
			// duration preserved, shifted to this occurrence's start. A
			// modification exception may still override both sides below.
			item.StartTime = cur
			item.EndTime = ev.EndTime.Add(cur.Sub(anchor))
			if x, ok := exc[cur]; ok {
				if x.Deleted {
					next, alive := nextOccurrence(cur, anchor, r, count)
					if !alive {
						break
					}
					cur = next
					continue
				}
				item = applyException(item, x)
			}
			out = append(out, item)
		}
		next, alive := nextOccurrence(cur, anchor, r, count)
		if !alive {
			break
		}
		cur = next
	}
	return out
}

// ruleSatisfiable reports whether r is a rule the generator can walk: a
// known type with the fields that type needs. Rules failing this check
// produce no occurrences at all — not even the anchor.
func ruleSatisfiable(r *Recurrence) bool {
	switch r.Type {
	case RecurrenceDaily:
		return true
	case RecurrenceWeekly:
		return r.DayOfWeek&0x7F != 0
	case RecurrenceMonthlyDate:
		return r.DayOfMonth >= 1
	case RecurrenceMonthlyByDay:
		return r.DayOfWeek&0x7F != 0 && r.WeekOfMonth >= 1 && r.WeekOfMonth <= 5
	case RecurrenceYearlyDate:
		return r.MonthOfYear >= 1 && r.MonthOfYear <= 12 && r.DayOfMonth >= 1
	case RecurrenceYearlyByDay:
		return r.MonthOfYear >= 1 && r.MonthOfYear <= 12 && r.DayOfWeek&0x7F != 0 && r.WeekOfMonth >= 1 && r.WeekOfMonth <= 5
	default:
		return false
	}
}

// nextOccurrence returns the next occurrence after cur for rule r. The
// second result is false when the series has ended (Until passed or the
// Occurrences budget exhausted). anchor is the series' first occurrence,
// needed by the weekly rule to keep its phase; count is the number of
// occurrences seen so far including cur, used to enforce the Occurrences
// limit.
func nextOccurrence(cur, anchor time.Time, r *Recurrence, count int) (time.Time, bool) {
	// Occurrences counts instances of the series starting with the anchor,
	// so the (N+1)-th instance exists only while count+1 <= Occurrences.
	if r.Occurrences > 0 && count >= r.Occurrences {
		return time.Time{}, false
	}
	next, ok := advanceDate(cur, anchor, r)
	if !ok {
		return time.Time{}, false
	}
	// Until names the last day on which an instance may occur; compare by
	// calendar date so an instance later in the day than the (typically
	// midnight) Until value still counts as on its day.
	if !r.Until.IsZero() {
		untilEnd := time.Date(r.Until.Year(), r.Until.Month(), r.Until.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		if !next.Before(untilEnd) {
			return time.Time{}, false
		}
	}
	return next, true
}

// advanceDate computes the next occurrence date after cur, keeping the
// anchor's time-of-day. It returns false for rules it cannot satisfy (unknown
// type, empty day mask, missing month/day fields).
func advanceDate(cur, anchor time.Time, r *Recurrence) (time.Time, bool) {
	switch r.Type {
	case RecurrenceDaily:
		return cur.AddDate(0, 0, r.stepDays()), true
	case RecurrenceWeekly:
		return nextWeeklyDay(cur, anchor, r)
	case RecurrenceMonthlyDate:
		return nextMonthlyDate(cur, r)
	case RecurrenceMonthlyByDay:
		return nthDayOfMonthAfter(cur, r.WeekOfMonth, r.DayOfWeek, r.Interval)
	case RecurrenceYearlyDate:
		return nextYearlyDate(cur, r)
	case RecurrenceYearlyByDay:
		return nthDayInMonthAfterYear(cur, r.MonthOfYear, r.WeekOfMonth, r.DayOfWeek, r.Interval)
	default:
		return time.Time{}, false
	}
}

// stepDays normalizes the interval to a positive step.
func (r *Recurrence) stepDays() int {
	if r.Interval <= 0 {
		return 1
	}
	return r.Interval
}

// nextWeeklyDay finds the next masked weekday strictly after cur, honoring
// the Interval-week phase fixed by the anchor. The series lives on an
// absolute week grid: a calendar week participates iff its index differs
// from the anchor week's index by a multiple of Interval. Within each
// participating week every masked weekday is an occurrence (ascending). This
// handles multi-day masks (e.g. Mon+Thu) correctly — a naive "+7*Interval"
// jump would skip same-week days.
func nextWeeklyDay(cur, anchor time.Time, r *Recurrence) (time.Time, bool) {
	mask := r.DayOfWeek
	if mask&0x7F == 0 {
		return time.Time{}, false
	}
	interval := r.stepDays()
	tod := timeOfDay(cur)
	curWeek := weekIndex(cur)
	anchorWeek := weekIndex(anchor)
	// Start from the current week (same-week masked days after cur still
	// belong to the series); snap forward to the next grid week if the
	// current week does not participate.
	w := curWeek
	delta := w - anchorWeek
	if delta%interval != 0 {
		w += interval - delta%interval
	}
	// Bounded even though a valid mask matches within one grid week: a
	// mask of only out-of-range bits must not spin here forever.
	for i := 0; i < 2*interval+2; i, w = i+1, w+interval {
		sunday := weekEpoch.AddDate(0, 0, 7*w) // Sunday opening week w
		for i := 0; i < 7; i++ {
			d := sunday.AddDate(0, 0, i).Add(tod)
			if !dowMatches(d, mask) {
				continue
			}
			if d.After(cur) {
				return d, true
			}
		}
	}
	return time.Time{}, false
}

// dowMatches reports whether d's weekday is in the mask.
func dowMatches(d time.Time, mask DayOfWeek) bool {
	var bit DayOfWeek
	switch d.Weekday() {
	case time.Sunday:
		bit = DowSunday
	case time.Monday:
		bit = DowMonday
	case time.Tuesday:
		bit = DowTuesday
	case time.Wednesday:
		bit = DowWednesday
	case time.Thursday:
		bit = DowThursday
	case time.Friday:
		bit = DowFriday
	case time.Saturday:
		bit = DowSaturday
	}
	return mask&bit != 0
}

// nextMonthlyDate advances to the same DayOfMonth Interval months later. If
// the target month lacks that day (e.g. the 31st in a 30-day month) the
// occurrence is skipped entirely — the next candidate is another Interval
// months further — matching Outlook/Exchange behaviour rather than clamping
// to month-end. Candidates are enumerated by month and checked against the
// month's actual length (day 0 of the following month), so a clamped date can
// never wedge the arithmetic: for "every 31st" the series runs
// Jan 31 → Mar 31 → May 31 → …, silently dropping Feb/Apr/Jun/Sep/Nov.
func nextMonthlyDate(cur time.Time, r *Recurrence) (time.Time, bool) {
	if r.DayOfMonth < 1 {
		return time.Time{}, false
	}
	interval := r.stepDays()
	tod := timeOfDay(cur)
	y, mon, _ := cur.Date()
	m := int(mon)
	for i := 0; i < 24*interval+12; i++ {
		m += interval
		yy, mm := yearMonth(y, m)
		dim := time.Date(yy, time.Month(mm)+1, 0, 0, 0, 0, 0, time.UTC).Day() // days in month mm
		if r.DayOfMonth <= dim {
			return time.Date(yy, time.Month(mm), r.DayOfMonth, 0, 0, 0, 0, time.UTC).Add(tod), true
		}
	}
	return time.Time{}, false
}

// nthDayOfMonthAfter returns the WeekOfMonth-th DayOfWeek day of the month
// Interval months after cur's month (WeekOfMonth 5 = last). cur itself is
// never returned.
func nthDayOfMonthAfter(cur time.Time, weekOfMonth int, dow DayOfWeek, interval int) (time.Time, bool) {
	if dow&0x7F == 0 || weekOfMonth < 1 || weekOfMonth > 5 {
		return time.Time{}, false
	}
	if interval <= 0 {
		interval = 1
	}
	tod := timeOfDay(cur)
	y, mon, _ := cur.Date()
	m := int(mon) + interval
	// Every month holds at least four of each weekday, so a valid rule
	// resolves within a couple of steps; the ceiling keeps a degenerate
	// one from spinning.
	y2, m2 := yearMonth(y, m)
	for i := 0; i < 2*interval+12; i++ {
		// nthDayOfMonth returns midnight UTC; the strictly-after comparison
		// is date-based, and the anchor's clock time is re-applied to the
		// winner so every occurrence shares the anchor's time-of-day.
		if d, ok := nthDayOfMonth(y2, time.Month(m2), weekOfMonth, dow); ok {
			if d.After(cur) {
				return d.Add(tod), true
			}
		}
		y2, m2 = yearMonth(y2, m2+1)
	}
	return time.Time{}, false
}

// nthDayInMonthAfterYear is the yearly analogue: WeekOfMonth-th DayOfWeek of
// MonthOfYear, Interval years after cur's year.
func nthDayInMonthAfterYear(cur time.Time, monthOfYear, weekOfMonth int, dow DayOfWeek, interval int) (time.Time, bool) {
	if monthOfYear < 1 || monthOfYear > 12 || dow&0x7F == 0 || weekOfMonth < 1 || weekOfMonth > 5 {
		return time.Time{}, false
	}
	if interval <= 0 {
		interval = 1
	}
	tod := timeOfDay(cur)
	y, _, _ := cur.Date()
	// A valid rule resolves within a few years; the ceiling keeps a
	// degenerate one from spinning (see nthDayOfMonthAfter).
	for i := 0; i < 4*interval+8; i++ {
		y += interval
		// Midnight-UTC comparison, anchor clock time re-applied (see
		// nthDayOfMonthAfter).
		if d, ok := nthDayOfMonth(y, time.Month(monthOfYear), weekOfMonth, dow); ok {
			if d.After(cur) {
				return d.Add(tod), true
			}
		}
	}
	return time.Time{}, false
}

// nthDayOfMonth finds the n-th (or last, n==5) day of the week `dow` in the
// given month/year. All times are midnight UTC so comparisons are date-based.
func nthDayOfMonth(year int, month time.Month, n int, dow DayOfWeek) (time.Time, bool) {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, 0).AddDate(0, 0, -1)
	var found []time.Time
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if dowMatches(d, dow) {
			found = append(found, d)
		}
	}
	if len(found) == 0 {
		return time.Time{}, false
	}
	if n == 5 {
		return found[len(found)-1], true
	}
	if n < 1 || n > len(found) {
		return time.Time{}, false
	}
	return found[n-1], true
}

// nextYearlyDate advances to MonthOfYear/DayOfMonth Interval years later,
// skipping years where the date does not exist (Feb 29 in non-leap years).
// Each candidate is recomputed from the fixed anchor year, never via a
// cumulative AddDate, so a clamped date cannot wedge the search.
func nextYearlyDate(cur time.Time, r *Recurrence) (time.Time, bool) {
	if r.MonthOfYear < 1 || r.MonthOfYear > 12 || r.DayOfMonth < 1 {
		return time.Time{}, false
	}
	tod := timeOfDay(cur)
	y, _, _ := cur.Date()
	interval := r.stepDays()
	for i := 0; i < 8*interval+8; i++ {
		cy := y + interval*(i+1)
		cand := time.Date(cy, time.Month(r.MonthOfYear), r.DayOfMonth, 0, 0, 0, 0, time.UTC)
		if cand.Day() == r.DayOfMonth && cand.Month() == time.Month(r.MonthOfYear) {
			return cand.Add(tod), true
		}
	}
	return time.Time{}, false
}

// yearMonth normalizes a possibly out-of-range month into (year, month).
func yearMonth(year int, month int) (int, int) {
	for month < 1 {
		month += 12
		year--
	}
	for month > 12 {
		month -= 12
		year++
	}
	return year, month
}

// applyException overrides an occurrence's fields from a modification
// exception. Only present values win: text fields when non-empty, numeric
// fields when non-zero, and the time window only when BOTH sides are given.
func applyException(item EventItem, x Exception) EventItem {
	if x.Subject != "" {
		item.Subject = x.Subject
	}
	if x.Location != "" {
		item.Location = x.Location
	}
	if x.Body != "" {
		item.Body = x.Body
	}
	if x.BusyStatus != 0 {
		item.BusyStatus = x.BusyStatus
	}
	if x.Reminder != 0 {
		item.Reminder = x.Reminder
	}
	if x.AllDayEvent {
		item.AllDayEvent = true
	}
	if !x.StartTime.IsZero() && !x.EndTime.IsZero() {
		item.StartTime = x.StartTime
		item.EndTime = x.EndTime
	}
	return item
}

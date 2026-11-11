// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// TestBuildEventAppEmitsEmptyClearedFields guards the clear mechanism: a
// cleared field must be serialized as an explicit empty element, because
// omitting it would leave the server's stored value intact (Exchange
// replaces the whole item on a Change).
func TestBuildEventAppEmitsEmptyClearedFields(t *testing.T) {
	draft := EventDraft{
		Subject:     "keep",
		StartTime:   time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
		ClearFields: []string{"location", "attendees", "reminder"},
	}
	app := buildEventApp(draft)

	loc := app.Find("Location")
	if loc == nil {
		t.Fatal("no Location element for a cleared location")
	}
	if len(loc.Children) != 1 {
		t.Fatalf("cleared Location has %d children, want 1 (an empty text child)", len(loc.Children))
	}
	if txt, ok := loc.Children[0].(wbxml.Text); !ok || string(txt) != "" {
		t.Errorf("cleared Location child = %#v, want empty wbxml.Text", loc.Children[0])
	}

	atts := app.Find("Attendees")
	if atts == nil {
		t.Fatal("no Attendees element for a cleared attendee list")
	}
	if len(atts.Children) != 0 {
		t.Errorf("cleared Attendees has %d children, want 0", len(atts.Children))
	}

	rem := app.Find("Reminder")
	if rem == nil {
		t.Fatal("no Reminder element for a cleared reminder")
	}
	if got := rem.TextContent(); got != "0" {
		t.Errorf("cleared Reminder = %q, want \"0\"", got)
	}

	// Control: a non-cleared field is still serialized normally.
	if got := app.Find("Subject").TextContent(); got != "keep" {
		t.Errorf("Subject = %q, want keep", got)
	}
}

// TestBuildEventApp_omitsUnsetUnclearedFields verifies that a field with
// neither a value nor a clear request is omitted entirely (the pre-existing
// behavior the || conditions must not break), and that a valued, non-cleared
// field still serializes its value.
func TestBuildEventApp_omitsUnsetUnclearedFields(t *testing.T) {
	start := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	// Nothing set, nothing cleared -> Location/Attendees/Reminder absent.
	app := buildEventApp(EventDraft{Subject: "x", StartTime: start, EndTime: start.Add(time.Hour)})
	if app.Find("Location") != nil {
		t.Error("Location emitted though unset and uncleared")
	}
	if app.Find("Attendees") != nil {
		t.Error("Attendees emitted though unset and uncleared")
	}
	if app.Find("Reminder") != nil {
		t.Error("Reminder emitted though unset and uncleared")
	}
	// Valued, non-cleared -> value serialized normally.
	app = buildEventApp(EventDraft{Subject: "x", Location: "Room 1", StartTime: start, EndTime: start.Add(time.Hour)})
	if got := app.Find("Location").TextContent(); got != "Room 1" {
		t.Errorf("Location = %q, want Room 1", got)
	}
}

// TestMergeEventDraftClear verifies that clearing zeroes the affected fields
// while preserving untouched ones, and that the clear list is carried through
// so buildEventApp can emit the explicit empty elements.
func TestMergeEventDraftClear(t *testing.T) {
	current := EventItem{
		Subject:     "keep",
		Location:    "old-link",
		BusyStatus:  2,
		Sensitivity: 1,
		Reminder:    15,
		UID:         "abc",
		Attendees:   []EventAttendee{{Email: "a@x"}},
	}

	m := mergeEventDraft(current, EventDraft{
		ClearFields: []string{"location", "attendees", "reminder", "busy_status", "sensitivity"},
	})
	if m.Location != "" {
		t.Errorf("Location not cleared: %q", m.Location)
	}
	if len(m.Attendees) != 0 {
		t.Errorf("Attendees not cleared: %+v", m.Attendees)
	}
	if m.Reminder != 0 {
		t.Errorf("Reminder not cleared: %d", m.Reminder)
	}
	if m.BusyStatus != 0 {
		t.Errorf("BusyStatus not cleared: %d", m.BusyStatus)
	}
	if m.Sensitivity != 0 {
		t.Errorf("Sensitivity not cleared: %d", m.Sensitivity)
	}
	// Untouched fields are preserved.
	if m.Subject != "keep" || m.UID != "abc" {
		t.Errorf("untouched fields lost: subj=%q uid=%q", m.Subject, m.UID)
	}
	// The clear list is carried through for buildEventApp.
	if len(m.ClearFields) != 5 {
		t.Errorf("ClearFields not carried: %+v", m.ClearFields)
	}

	// A clear must win even when the same field is also supplied as a value.
	m = mergeEventDraft(current, EventDraft{
		Location:    "Room 1",
		ClearFields: []string{"location"},
	})
	if m.Location != "" {
		t.Errorf("clear did not win over a same-field draft value: %q", m.Location)
	}
}

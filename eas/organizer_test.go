// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// TestBuildEventApp_EmitsOrganizerForMeeting pins the fix for Exchange
// rejecting a meeting Add with item status 6 (ConversionError): an item
// with attendees must carry OrganizerEmail (and OrganizerName when set),
// positioned after MeetingStatus and before Recurrence in tag order.
func TestBuildEventApp_EmitsOrganizerForMeeting(t *testing.T) {
	start := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	app := buildEventApp(EventDraft{
		Subject:        "Meeting",
		StartTime:      start,
		EndTime:        start.Add(time.Hour),
		Attendees:      []EventAttendee{{Email: "guest@x.com"}},
		OrganizerEmail: "boss@x.com",
		OrganizerName:  "The Boss",
	})
	if got := app.Find("OrganizerEmail").TextContent(); got != "boss@x.com" {
		t.Errorf("OrganizerEmail = %q, want boss@x.com", got)
	}
	if got := app.Find("OrganizerName").TextContent(); got != "The Boss" {
		t.Errorf("OrganizerName = %q, want The Boss", got)
	}
	// MS-ASAIRS tag order: MeetingStatus 0x18 < OrganizerEmail 0x19 <
	// OrganizerName 0x1A < Recurrence 0x1B. Verify relative positions.
	names := make([]string, 0, len(app.Children))
	for _, ch := range app.Children {
		if el, ok := ch.(*wbxml.Element); ok {
			names = append(names, el.Name)
		}
	}
	pos := func(name string) int {
		for i, n := range names {
			if n == name {
				return i
			}
		}
		return -1
	}
	if pos("OrganizerEmail") < pos("MeetingStatus") {
		t.Error("OrganizerEmail emitted before MeetingStatus")
	}
	if pos("OrganizerName") < pos("OrganizerEmail") {
		t.Error("OrganizerName emitted before OrganizerEmail")
	}
}

// TestBuildEventApp_NoOrganizerForAppointmentOrWithoutEmail verifies the
// organizer elements stay absent for a plain appointment (no attendees) and
// when no organizer address is known — the pre-fix shape that Exchange
// accepts for appointments.
func TestBuildEventApp_NoOrganizerForAppointmentOrWithoutEmail(t *testing.T) {
	start := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	app := buildEventApp(EventDraft{
		Subject:        "Solo",
		StartTime:      start,
		EndTime:        start.Add(time.Hour),
		OrganizerEmail: "boss@x.com", // set, but no attendees -> not a meeting
	})
	if app.Find("OrganizerEmail") != nil {
		t.Error("OrganizerEmail emitted for an appointment without attendees")
	}
	app = buildEventApp(EventDraft{
		Subject:   "Meeting",
		StartTime: start,
		EndTime:   start.Add(time.Hour),
		Attendees: []EventAttendee{{Email: "guest@x.com"}},
		// No organizer address: nothing to emit.
	})
	if app.Find("OrganizerEmail") != nil {
		t.Error("OrganizerEmail emitted without an address")
	}
}

// TestMergeEventDraft_Organizer verifies the organizer round-trips from the
// fetched server state and that a draft value overrides it.
func TestMergeEventDraft_Organizer(t *testing.T) {
	current := EventItem{
		Subject:        "keep",
		Attendees:      []EventAttendee{{Email: "a@x"}},
		OrganizerEmail: "boss@x.com",
		OrganizerName:  "The Boss",
	}
	m := mergeEventDraft(current, EventDraft{})
	if m.OrganizerEmail != "boss@x.com" || m.OrganizerName != "The Boss" {
		t.Errorf("organizer lost on merge: %q %q", m.OrganizerEmail, m.OrganizerName)
	}
	m = mergeEventDraft(current, EventDraft{OrganizerEmail: "new@x.com"})
	if m.OrganizerEmail != "new@x.com" {
		t.Errorf("draft organizer did not override: %q", m.OrganizerEmail)
	}
}

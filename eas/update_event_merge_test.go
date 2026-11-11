// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// TestUpdateEventPreservesUnchangedFields guards against the partial-update
// data-loss bug: Exchange replaces the whole item on a calendar Change, so a
// caller that changes only the location must still have subject, attendees,
// and the iCal UID re-sent or they are dropped server-side. The fix makes
// UpdateEvent a read-modify-write; this test asserts the outgoing Change
// carries the fetched fields even though the draft supplied only Location.
func TestUpdateEventPreservesUnchangedFields(t *testing.T) {
	const (
		folder = "23"
		id     = "23:24"
		subj   = "Прогон Демо"
		email  = "amalinovskiy@usetech.ru"
		uid    = "11111111-2222-3333-4444-555555555555"
		loc    = "https://mj.usetech.ru/auth/AI_general"
	)

	var changeApp *wbxml.Element
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := r.URL.Query().Get("Cmd")
		switch cmd {
		case "ItemOperations":
			// FetchEvent: return the event's current authoritative state.
			resp := &wbxml.Document{Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Response",
					wbxml.E(wbxml.PageItemOperations, "Fetch",
						wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folder)),
						wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(id)),
						wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageItemOperations, "Properties",
							wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text(subj)),
							wbxml.E(wbxml.PageCalendar, "Location", wbxml.Text("old-location")),
							wbxml.E(wbxml.PageCalendar, "StartTime", wbxml.Text("20261112T080000Z")),
							wbxml.E(wbxml.PageCalendar, "EndTime", wbxml.Text("20261112T090000Z")),
							wbxml.E(wbxml.PageCalendar, "BusyStatus", wbxml.Text("2")),
							wbxml.E(wbxml.PageCalendar, "MeetingStatus", wbxml.Text("1")),
							wbxml.E(wbxml.PageCalendar, "UID", wbxml.Text(uid)),
							wbxml.E(wbxml.PageCalendar, "Attendees",
								wbxml.E(wbxml.PageCalendar, "Attendee",
									wbxml.E(wbxml.PageCalendar, "Email", wbxml.Text(email)),
									wbxml.E(wbxml.PageCalendar, "Name", wbxml.Text("Александр Малиновский")),
								),
							),
						),
					),
				),
			)}
			writeWBXML(t, w, resp)
		case "Sync":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read Sync request: %v", err)
				return
			}
			req, err := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
			if err != nil {
				t.Errorf("decode Sync request: %v", err)
				return
			}
			changeApp = req.Root.Find("ApplicationData")
			resp := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Collections",
					wbxml.E(wbxml.PageAirSync, "Collection",
						wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("next-key")),
						wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folder)),
						wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")))))}
			writeWBXML(t, w, resp)
		default:
			t.Errorf("unexpected command %q", cmd)
		}
	}))
	t.Cleanup(server.Close)

	state := NewMemoryState()
	if err := state.SetSyncKey(context.Background(), folder, "existing-key"); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{
		ServerURL: server.URL, Username: "user", Password: "password",
		DeviceID: "0123456789abcdef0123456789abcdef", DeviceType: "MCP",
		ASVersion: "14.1", State: state,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Only the location is changed — no subject, attendees, or times.
	err = client.UpdateEvent(context.Background(), folder, id, EventDraft{
		Location: loc,
	})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if changeApp == nil {
		t.Fatal("no ApplicationData captured from the Change request")
	}

	if got := changeApp.Find("Subject").TextContent(); got != subj {
		t.Errorf("Change Subject = %q, want preserved %q", got, subj)
	}
	if got := changeApp.Find("Location").TextContent(); got != loc {
		t.Errorf("Change Location = %q, want updated %q", got, loc)
	}
	if got := changeApp.Find("UID").TextContent(); got != uid {
		t.Errorf("Change UID = %q, want preserved %q (new UID would orphan the meeting)", got, uid)
	}
	atts := changeApp.Find("Attendees")
	if atts == nil {
		t.Fatal("Change has no Attendees element — attendee was dropped")
	}
	if got := atts.Find("Email").TextContent(); got != email {
		t.Errorf("Change attendee Email = %q, want preserved %q", got, email)
	}
}

// TestMergeEventDraft verifies the field-merge rules directly, independent of
// the wire round-trip.
func TestMergeEventDraft(t *testing.T) {
	current := EventItem{
		Subject:     "keep-me",
		Location:    "old",
		Body:        "body",
		StartTime:   time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
		BusyStatus:  2,
		UID:         "abc",
		TimeZoneRaw: "rawtz",
		Attendees:   []EventAttendee{{Email: "a@x"}},
	}

	// Empty draft → everything preserved.
	m := mergeEventDraft(current, EventDraft{})
	if m.Subject != "keep-me" || m.Location != "old" || m.UID != "abc" || m.BusyStatus != 2 {
		t.Errorf("empty draft did not preserve current state: %+v", m)
	}
	if len(m.Attendees) != 1 || m.Attendees[0].Email != "a@x" {
		t.Errorf("empty draft dropped attendees: %+v", m.Attendees)
	}

	// Partial draft → only supplied fields change.
	m = mergeEventDraft(current, EventDraft{Location: "new"})
	if m.Location != "new" || m.Subject != "keep-me" {
		t.Errorf("partial draft wrong: loc=%q subj=%q", m.Location, m.Subject)
	}

	// Timing is all-or-nothing: supplying only one side keeps the old window.
	m = mergeEventDraft(current, EventDraft{StartTime: time.Now()})
	if !m.StartTime.Equal(current.StartTime) || !m.EndTime.Equal(current.EndTime) {
		t.Errorf("single-sided time update should keep the existing window, got %v..%v", m.StartTime, m.EndTime)
	}
}

func writeWBXML(t *testing.T, w http.ResponseWriter, doc *wbxml.Document) {
	t.Helper()
	encoded, err := wbxml.Marshal(doc, wbxml.DefaultRegistry())
	if err != nil {
		t.Errorf("encode response: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	_, _ = w.Write(encoded)
}

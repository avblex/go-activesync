// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// EventItem is a parsed calendar event from a calendar Sync or
// ItemOperations Fetch response. Fields populated only when the server
// included them.
type EventItem struct {
	ServerID string
	UID      string

	Subject       string
	Location      string
	Body          string
	BodyType      BodyType
	StartTime     time.Time
	EndTime       time.Time
	AllDayEvent   bool
	BusyStatus    int
	Sensitivity   int
	MeetingStatus int
	Reminder      int

	OrganizerName  string
	OrganizerEmail string
	Attendees      []EventAttendee

	// Recurrence is the repeat rule, when set.
	Recurrence *Recurrence
	// Exceptions are per-instance overrides.
	Exceptions []Exception
	// TimeZone is the parsed Microsoft TIME_ZONE_INFORMATION blob.
	// Zero value means the server didn't include one (Outlook fills
	// this in for recurring events; one-shot events often omit it).
	TimeZone EASTimeZone
	// TimeZoneRaw is the original base64 text from the server, useful
	// for byte-identical round-trip when updating the event.
	TimeZoneRaw string
}

// EventAttendee describes one attendee on a calendar event.
type EventAttendee struct {
	Name           string
	Email          string
	AttendeeStatus int
	AttendeeType   int
}

// EventDraft is the input to CreateEvent / UpdateEvent. All fields are
// optional except StartTime and EndTime; absent fields become "no change"
// on update or use sensible defaults on create.
type EventDraft struct {
	// UID is the calendar item's iCal UID (GUID). Leave empty on create:
	// a fresh GUID is generated. On update, carry the value the server
	// reported so the meeting keeps its identity across a Change —
	// Exchange replaces the whole item, and a new UID orphans the
	// invitation / attendee association.
	UID         string
	Subject     string
	Location    string
	Body        string
	StartTime   time.Time
	EndTime     time.Time
	AllDayEvent bool
	BusyStatus  int // 0=free, 1=tentative, 2=busy, 3=OOF, 4=working elsewhere
	Sensitivity int // 0=normal, 1=personal, 2=private, 3=confidential
	Reminder    int // minutes before; 0 = none
	Attendees   []EventAttendee
	// OrganizerEmail/Name identify the meeting organizer. Exchange
	// requires OrganizerEmail on Add whenever the item has attendees
	// (MeetingStatus 1); omitting it fails with item status 6
	// (ConversionError). The organizer is the mailbox owner, so callers
	// typically pass the account's own address.
	OrganizerEmail string
	OrganizerName  string
	// Recurrence sets a repeat rule; nil means single-instance.
	Recurrence *Recurrence
	// Exceptions list per-instance overrides for a recurring event.
	Exceptions []Exception
	// TimeZone is the EAS time zone blob. EncodeTimeZone(loc) builds
	// one from a Go *time.Location for the simple case. When zero,
	// no TimeZone element is sent (the server uses its default).
	TimeZone *EASTimeZone
	// TimeZoneRaw is an alternative way to specify the time zone:
	// supply the base64 string the server gave you on a previous
	// fetch (for byte-identical round-trip).
	TimeZoneRaw string
	// ClearFields lists field names to reset to their default on update:
	// "location", "attendees", "reminder", "sensitivity", "busy_status".
	// A zero value means "leave unchanged" in the read-modify-write merge, so
	// a caller cannot express "clear this field" by simply omitting it.
	// For location/attendees/reminder buildEventApp emits an explicit
	// empty-or-zero element; busy_status/sensitivity are always serialized
	// and are simply sent zeroed. For the clear to reach the server the
	// update merge must also zero the corresponding draft value so the
	// Change carries the cleared state.
	ClearFields []string
}

// CalendarSyncOptions controls a SyncCalendar request.
type CalendarSyncOptions struct {
	WindowSize  int
	DateFilter  FilterType
	NoBootstrap bool
}

// CalendarSyncResult is the parsed output of a calendar Sync.
type CalendarSyncResult struct {
	SyncKey       string
	MoreAvailable bool
	Added         []EventItem
	Changed       []EventItem
	Deleted       []string
}

// SyncCalendar issues a Sync command for a calendar folder and parses
// EventItems from the response.
func (c *httpClient) SyncCalendar(ctx context.Context, folderID string, opts CalendarSyncOptions) (*CalendarSyncResult, error) {
	if folderID == "" {
		return nil, errors.New("eas: SyncCalendar: folderID is required")
	}
	if opts.WindowSize <= 0 {
		opts.WindowSize = 100
	}
	if opts.DateFilter == 0 {
		opts.DateFilter = FilterTwoWeek
	}

	requestKey, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("eas: SyncCalendar: read key: %w", err)
	}
	res, err := c.syncCalendarOnce(ctx, folderID, opts)
	if err != nil && IsStatusCode(err, StatusInvalidSyncKey) {
		if rerr := c.cfg.State.SetSyncKey(ctx, folderID, "0"); rerr != nil {
			return nil, fmt.Errorf("eas: SyncCalendar: reset key: %w", rerr)
		}
		requestKey = "0"
		res, err = c.syncCalendarOnce(ctx, folderID, opts)
	}
	if err != nil {
		return nil, err
	}
	if !opts.NoBootstrap && requestKey == "0" {
		return c.syncCalendarOnce(ctx, folderID, opts)
	}
	return res, nil
}

func (c *httpClient) syncCalendarOnce(ctx context.Context, folderID string, opts CalendarSyncOptions) (*CalendarSyncResult, error) {
	key, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("eas: SyncCalendar: read key: %w", err)
	}
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
	)
	if key != "0" {
		collection.Children = append(collection.Children,
			wbxml.E(wbxml.PageAirSync, "DeletesAsMoves", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSync, "GetChanges", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSync, "WindowSize", wbxml.Text(itoa(opts.WindowSize))),
			wbxml.E(wbxml.PageAirSync, "Options",
				wbxml.E(wbxml.PageAirSync, "FilterType", wbxml.Text(itoa(int(opts.DateFilter)))),
			),
		)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	resp, err := c.post(ctx, "Sync", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return &CalendarSyncResult{SyncKey: key}, nil
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Sync", Code: st}
	}
	respCollection := resp.Root.Find("Collection")
	if respCollection == nil {
		return nil, errors.New("eas: SyncCalendar: response missing <Collection>")
	}
	// A server may hand us a fresh SyncKey alongside a failed collection
	// Status (MS-ASCMD §2.2.3.29: SyncKey and Status are siblings). Persist
	// it before bailing out: dropping it strands the server-side sync state
	// and the next request fails with 132/135 (SyncStateNotFound /
	// SyncStateAlreadyExists) instead of recovering with a plain resync.
	var respKey string
	if k := findShallow(respCollection, "SyncKey", 1); k != nil {
		respKey = k.TextContent()
	}
	if cs := findShallow(respCollection, "Status", 1); cs != nil {
		if code := atoi(cs.TextContent()); code != 0 && code != StatusOK {
			if respKey != "" {
				if err := c.cfg.State.SetSyncKey(ctx, folderID, respKey); err != nil {
					return nil, fmt.Errorf("eas: SyncCalendar: persist key: %w", err)
				}
			}
			return nil, &StatusError{Command: "Sync", Code: code}
		}
	}
	out := &CalendarSyncResult{SyncKey: respKey}
	if findShallow(respCollection, "MoreAvailable", 1) != nil {
		out.MoreAvailable = true
	}
	if cmds := findShallow(respCollection, "Commands", 1); cmds != nil {
		for _, c := range cmds.Children {
			el, ok := c.(*wbxml.Element)
			if !ok {
				continue
			}
			switch el.Name {
			case "Add":
				out.Added = append(out.Added, parseEventFromCommand(el))
			case "Change":
				out.Changed = append(out.Changed, parseEventFromCommand(el))
			case "Delete", "SoftDelete":
				if id := el.Find("ServerId"); id != nil {
					out.Deleted = append(out.Deleted, id.TextContent())
				}
			}
		}
	}
	if out.SyncKey == "" {
		return nil, errors.New("eas: SyncCalendar: response missing SyncKey")
	}
	if err := c.cfg.State.SetSyncKey(ctx, folderID, out.SyncKey); err != nil {
		return nil, fmt.Errorf("eas: SyncCalendar: persist key: %w", err)
	}
	return out, nil
}

func parseEventFromCommand(el *wbxml.Element) EventItem {
	out := EventItem{}
	if id := el.Find("ServerId"); id != nil {
		out.ServerID = id.TextContent()
	}
	app := el.Find("ApplicationData")
	if app == nil {
		return out
	}
	for _, c := range app.Children {
		ce, ok := c.(*wbxml.Element)
		if !ok {
			continue
		}
		switch ce.Codepage {
		case wbxml.PageCalendar:
			parseCalendarField(&out, ce)
		case wbxml.PageAirSyncBase:
			if ce.Name == "Body" {
				parseEventBody(&out, ce)
			}
		}
	}
	return out
}

func parseCalendarField(out *EventItem, el *wbxml.Element) {
	switch el.Name {
	case "Subject":
		out.Subject = el.TextContent()
	case "Location":
		out.Location = el.TextContent()
	case "StartTime":
		out.StartTime, _ = parseEASTime(el.TextContent())
	case "EndTime":
		out.EndTime, _ = parseEASTime(el.TextContent())
	case "AllDayEvent":
		out.AllDayEvent = el.TextContent() == "1"
	case "BusyStatus":
		out.BusyStatus = atoi(el.TextContent())
	case "Sensitivity":
		out.Sensitivity = atoi(el.TextContent())
	case "MeetingStatus":
		out.MeetingStatus = atoi(el.TextContent())
	case "Reminder":
		out.Reminder = atoi(el.TextContent())
	case "OrganizerName":
		out.OrganizerName = el.TextContent()
	case "OrganizerEmail":
		out.OrganizerEmail = el.TextContent()
	case "UID":
		out.UID = el.TextContent()
	case "Attendees":
		for _, c := range el.Children {
			at, ok := c.(*wbxml.Element)
			if !ok || at.Name != "Attendee" {
				continue
			}
			a := EventAttendee{}
			if n := at.Find("Name"); n != nil {
				a.Name = n.TextContent()
			}
			if e := at.Find("Email"); e != nil {
				a.Email = e.TextContent()
			}
			if s := at.Find("AttendeeStatus"); s != nil {
				a.AttendeeStatus = atoi(s.TextContent())
			}
			if t := at.Find("AttendeeType"); t != nil {
				a.AttendeeType = atoi(t.TextContent())
			}
			out.Attendees = append(out.Attendees, a)
		}
	case "Recurrence":
		out.Recurrence = recurrenceFromWBXML(el)
	case "Exceptions":
		for _, c := range el.Children {
			if ex, ok := c.(*wbxml.Element); ok && ex.Name == "Exception" {
				out.Exceptions = append(out.Exceptions, parseException(ex))
			}
		}
	case "TimeZone":
		out.TimeZoneRaw = el.TextContent()
		if tz, err := DecodeTimeZone(out.TimeZoneRaw); err == nil {
			out.TimeZone = tz
		}
	}
}

func parseEventBody(out *EventItem, body *wbxml.Element) {
	for _, c := range body.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Codepage != wbxml.PageAirSyncBase {
			continue
		}
		switch el.Name {
		case "Type":
			out.BodyType = BodyType(atoi(el.TextContent()))
		case "Data":
			if op := firstOpaque(el); op != nil {
				out.Body = string(op)
			} else {
				out.Body = el.TextContent()
			}
		}
	}
}

// CreateEvent adds a new calendar event via Sync. Returns the
// server-assigned ServerID (or the temporary client id if the server
// didn't echo a new one).
func (c *httpClient) CreateEvent(ctx context.Context, folderID string, draft EventDraft) (string, error) {
	if folderID == "" {
		return "", errors.New("eas: CreateEvent: folderID is required")
	}
	if draft.StartTime.IsZero() || draft.EndTime.IsZero() {
		return "", errors.New("eas: CreateEvent: StartTime and EndTime are required")
	}
	if err := c.ensureSynced(ctx, folderID); err != nil {
		return "", err
	}
	clientID := orRandomID("")
	app := buildEventApp(draft)
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Add",
			wbxml.E(wbxml.PageAirSync, "ClientId", wbxml.Text(clientID)),
			app,
		),
	)
	resp, err := c.sendSyncCommands(ctx, folderID, cmds)
	if err != nil {
		return "", err
	}
	// Look for Responses/Add with ClientId match → return the server id.
	// MS-ASCMD §2.2.3.7: a failed Add carries its own <Status> (and no
	// ServerId) even when the collection Status is 1 — ignoring it made a
	// rejected create look like a success.
	if collection := resp.Find("Collection"); collection != nil {
		if responses := findShallow(collection, "Responses", 1); responses != nil {
			for _, r := range responses.Children {
				re, ok := r.(*wbxml.Element)
				if !ok || re.Name != "Add" {
					continue
				}
				cid := re.Find("ClientId")
				if cid == nil || cid.TextContent() != clientID {
					continue
				}
				if st := re.Find("Status"); st != nil {
					if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
						return "", &StatusError{Command: "Sync", Code: code}
					}
				}
				if sid := re.Find("ServerId"); sid != nil {
					return sid.TextContent(), nil
				}
				return "", errors.New("eas: CreateEvent: server accepted the Add but returned no ServerId")
			}
		}
	}
	return "", errors.New("eas: CreateEvent: server response missing Responses/Add for the sent ClientId")
}

// UpdateEvent modifies an existing event identified by serverID. It is a
// read-modify-write: the event's current state is fetched first and only
// the non-zero fields of draft are applied on top (fields listed in
// draft.ClearFields are additionally zeroed). This matters because
// Exchange replaces the whole item on a calendar Change — sending just the
// changed fields would silently drop everything else (subject, attendees,
// recurrence, time zone, ...).
func (c *httpClient) UpdateEvent(ctx context.Context, folderID, serverID string, draft EventDraft) error {
	if folderID == "" || serverID == "" {
		return errors.New("eas: UpdateEvent: folderID and serverID are required")
	}
	if err := c.ensureSynced(ctx, folderID); err != nil {
		return err
	}
	current, err := c.FetchEvent(ctx, folderID, serverID)
	if err != nil {
		return fmt.Errorf("eas: UpdateEvent: fetch current state: %w", err)
	}
	merged := mergeEventDraft(*current, draft)
	app := buildEventApp(merged)
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Change",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
			app,
		),
	)
	resp, err := c.sendSyncCommands(ctx, folderID, cmds)
	if err != nil {
		return err
	}
	return itemResponseError(resp, "Change", serverID)
}

// mergeEventDraft produces the full field set to send for a Change: the
// event's current server state with only the non-zero fields of draft
// applied on top. Because Exchange replaces the whole item on a calendar
// Change, every field left at its zero value would be dropped on the
// server; seeding from the fetched state keeps untouched fields (subject,
// attendees, recurrence, time zone, ...) intact.
//
// Zero values double as the "leave unchanged" sentinel, matching EventDraft's
// documented contract. A caller who wants to reset a field to its default
// instead lists it in `draft.ClearFields`; those fields are forced to their
// zero value below, overriding any value seeded from the fetched state.
func mergeEventDraft(current EventItem, draft EventDraft) EventDraft {
	merged := EventDraft{
		Subject:     current.Subject,
		Location:    current.Location,
		Body:        current.Body,
		StartTime:   current.StartTime,
		EndTime:     current.EndTime,
		AllDayEvent: current.AllDayEvent,
		BusyStatus:  current.BusyStatus,
		Sensitivity: current.Sensitivity,
		Reminder:    current.Reminder,
		Attendees:   current.Attendees,
		// Round-trip the organizer the server reported: a Change replaces
		// the whole item, and dropping the organizer from a meeting can
		// fail conversion the same way a missing one fails Add.
		OrganizerEmail: current.OrganizerEmail,
		OrganizerName:  current.OrganizerName,
		Recurrence:     current.Recurrence,
		Exceptions:     current.Exceptions,
		// Round-trip the raw time-zone blob the server reported (byte
		// identical). Do NOT fall back to the parsed current.TimeZone:
		// when the server omitted it that struct is zero-valued, and the
		// zero SYSTEMTIME blob is rejected by Exchange (item status 6).
		TimeZoneRaw: current.TimeZoneRaw,
		UID:         current.UID,
	}
	if draft.Subject != "" {
		merged.Subject = draft.Subject
	}
	if draft.Location != "" {
		merged.Location = draft.Location
	}
	if draft.Body != "" {
		merged.Body = draft.Body
	}
	// Timing is all-or-nothing per the tool contract: both times must be
	// supplied to change them, otherwise keep the current window. Using
	// just one side would leave the other at its zero value and produce an
	// invalid item (e.g. EndTime at the epoch), so a half-supplied pair is
	// treated as "no timing change".
	if !draft.StartTime.IsZero() && !draft.EndTime.IsZero() {
		merged.StartTime = draft.StartTime
		merged.EndTime = draft.EndTime
	}
	if draft.AllDayEvent {
		merged.AllDayEvent = true
	}
	if draft.BusyStatus != 0 {
		merged.BusyStatus = draft.BusyStatus
	}
	if draft.Sensitivity != 0 {
		merged.Sensitivity = draft.Sensitivity
	}
	if draft.Reminder > 0 {
		merged.Reminder = draft.Reminder
	}
	if len(draft.Attendees) > 0 {
		merged.Attendees = draft.Attendees
	}
	if draft.OrganizerEmail != "" {
		merged.OrganizerEmail = draft.OrganizerEmail
	}
	if draft.OrganizerName != "" {
		merged.OrganizerName = draft.OrganizerName
	}
	if draft.Recurrence != nil {
		merged.Recurrence = draft.Recurrence
	}
	if len(draft.Exceptions) > 0 {
		merged.Exceptions = draft.Exceptions
	}
	if draft.TimeZoneRaw != "" {
		merged.TimeZoneRaw = draft.TimeZoneRaw
	} else if draft.TimeZone != nil {
		merged.TimeZone = draft.TimeZone
		merged.TimeZoneRaw = ""
	}
	if draft.UID != "" {
		merged.UID = draft.UID
	}
	// Apply explicit clears last so they win over values seeded from the
	// fetched state. Zeroing here is not sufficient on its own for the
	// conditionally-serialized fields — buildEventApp must also emit an
	// explicit empty element, which it does based on ClearFields.
	for _, f := range draft.ClearFields {
		switch f {
		case "location":
			merged.Location = ""
		case "attendees":
			merged.Attendees = nil
		case "reminder":
			merged.Reminder = 0
		case "sensitivity":
			merged.Sensitivity = 0
		case "busy_status":
			merged.BusyStatus = 0
		}
	}
	merged.ClearFields = draft.ClearFields
	return merged
}

// DeleteEvent removes an event identified by serverID.
func (c *httpClient) DeleteEvent(ctx context.Context, folderID, serverID string) error {
	if folderID == "" || serverID == "" {
		return errors.New("eas: DeleteEvent: folderID and serverID are required")
	}
	if err := c.ensureSynced(ctx, folderID); err != nil {
		return err
	}
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Delete",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
		),
	)
	resp, err := c.sendSyncCommands(ctx, folderID, cmds)
	if err != nil {
		return err
	}
	return itemResponseError(resp, "Delete", serverID)
}

// itemResponseError inspects a Sync response for the per-item <Status> of
// the Responses/<cmdName> element matching serverID (MS-ASCMD §2.2.3.24,
// §2.2.3.42). A failed item carries its own Status even when the
// collection Status is 1. Lenient by design: a missing response element
// is not treated as an error — some servers omit it on success.
func itemResponseError(respRoot *wbxml.Element, cmdName, serverID string) error {
	if respRoot == nil {
		return nil
	}
	collection := respRoot.Find("Collection")
	if collection == nil {
		return nil
	}
	responses := findShallow(collection, "Responses", 1)
	if responses == nil {
		return nil
	}
	for _, r := range responses.Children {
		re, ok := r.(*wbxml.Element)
		if !ok || re.Name != cmdName {
			continue
		}
		if sid := re.Find("ServerId"); sid == nil || sid.TextContent() != serverID {
			continue
		}
		if st := re.Find("Status"); st != nil {
			if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
				return &StatusError{Command: "Sync", Code: code}
			}
		}
		return nil
	}
	return nil
}

// sendSyncCommands wraps a Commands subtree in a Sync request, sends it,
// updates the persisted SyncKey, and returns the response root.
func (c *httpClient) sendSyncCommands(ctx context.Context, folderID string, cmds *wbxml.Element) (*wbxml.Element, error) {
	key, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("eas: sendSyncCommands: read key: %w", err)
	}
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
		cmds,
	)
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	resp, err := c.post(ctx, "Sync", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, nil
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Sync", Code: st}
	}
	if respCollection := resp.Root.Find("Collection"); respCollection != nil {
		if cs := findShallow(respCollection, "Status", 1); cs != nil {
			if code := atoi(cs.TextContent()); code != 0 && code != StatusOK {
				return nil, &StatusError{Command: "Sync", Code: code}
			}
		}
		if newKey := findShallow(respCollection, "SyncKey", 1); newKey != nil {
			if err := c.cfg.State.SetSyncKey(ctx, folderID, newKey.TextContent()); err != nil {
				return nil, fmt.Errorf("eas: sendSyncCommands: persist key: %w", err)
			}
		}
	}
	return resp.Root, nil
}

// clears reports whether the draft asks to reset the named field to its
// default. buildEventApp uses it to force an explicit empty element for the
// conditionally-serialized fields (location, attendees, reminder), which
// would otherwise be omitted and left unchanged on the server.
func (d EventDraft) clears(name string) bool {
	return slices.Contains(d.ClearFields, name)
}

// buildEventApp renders a draft as an ApplicationData element. MS-ASAIRS
// §20 requires child elements in ascending tag-ID order; Exchange rejects
// unordered calendar items with item status 6 (ConversionError). Calendar
// page order: TimeZone 0x05, AllDayEvent 0x06, Attendees 0x07, BusyStatus
// 0x0D, EndTime 0x12, Exceptions 0x14, Location 0x17, MeetingStatus 0x18,
// OrganizerEmail 0x19, OrganizerName 0x1A, Recurrence 0x1B, Reminder 0x24,
// Sensitivity 0x25, Subject 0x26,
// StartTime 0x27, UID 0x28. The AirSyncBase Body element follows last
// (separate codepage, sent after the calendar page content).
func buildEventApp(draft EventDraft) *wbxml.Element {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData")
	switch {
	case draft.TimeZoneRaw != "":
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "TimeZone",
			wbxml.Text(draft.TimeZoneRaw)))
	case draft.TimeZone != nil:
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "TimeZone",
			wbxml.Text(draft.TimeZone.Encode())))
	}
	// AllDayEvent (0x06) — required on Add; "0" for timed events.
	if draft.AllDayEvent {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "AllDayEvent", wbxml.Text("1")))
	} else {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "AllDayEvent", wbxml.Text("0")))
	}
	// Attendees (0x07). A cleared attendee list serializes as an empty
	// <Attendees/> (servers treat it as remove-all); because the list is
	// empty, MeetingStatus below also drops to 0, de-meeting the item.
	if len(draft.Attendees) > 0 || draft.clears("attendees") {
		atts := wbxml.E(wbxml.PageCalendar, "Attendees")
		for _, a := range draft.Attendees {
			at := wbxml.E(wbxml.PageCalendar, "Attendee")
			if a.Email != "" {
				at.Children = append(at.Children, wbxml.E(wbxml.PageCalendar, "Email", wbxml.Text(a.Email)))
			}
			if a.Name != "" {
				at.Children = append(at.Children, wbxml.E(wbxml.PageCalendar, "Name", wbxml.Text(a.Name)))
			}
			// AttendeeStatus (0x29) / AttendeeType (0x2A): emitted when
			// known so an update round-trips the values the server
			// reported instead of dropping them.
			if a.AttendeeStatus != 0 {
				at.Children = append(at.Children, wbxml.E(wbxml.PageCalendar, "AttendeeStatus", wbxml.Text(itoa(a.AttendeeStatus))))
			}
			if a.AttendeeType != 0 {
				at.Children = append(at.Children, wbxml.E(wbxml.PageCalendar, "AttendeeType", wbxml.Text(itoa(a.AttendeeType))))
			}
			atts.Children = append(atts.Children, at)
		}
		app.Children = append(app.Children, atts)
	}
	// BusyStatus (0x0D), DTStamp (0x11), EndTime (0x12)
	app.Children = append(app.Children,
		wbxml.E(wbxml.PageCalendar, "BusyStatus", wbxml.Text(itoa(draft.BusyStatus))))
	// DTStamp is the item last-change stamp; Exchange requires it on
	// calendar Add, Z-Push does not.
	app.Children = append(app.Children,
		wbxml.E(wbxml.PageCalendar, "DTStamp", wbxml.Text(formatEASTime(time.Now().UTC()))))
	if !draft.EndTime.IsZero() {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "EndTime", wbxml.Text(formatEASTime(draft.EndTime))))
	}
	// Exceptions (0x14)
	if len(draft.Exceptions) > 0 {
		exc := wbxml.E(wbxml.PageCalendar, "Exceptions")
		for _, x := range draft.Exceptions {
			exc.Children = append(exc.Children, exceptionToWBXML(x))
		}
		app.Children = append(app.Children, exc)
	}
	// Location (0x17). A cleared location is sent as an empty element so the
	// server drops the stored value; omitting it would keep the old one.
	if draft.Location != "" || draft.clears("location") {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Location", wbxml.Text(draft.Location)))
	}
	// MeetingStatus (0x18) — required on Add. 0 = appointment,
	// 1 = meeting (attendees present, no responses yet).
	meetingStatus := 0
	if len(draft.Attendees) > 0 {
		meetingStatus = 1
	}
	app.Children = append(app.Children,
		wbxml.E(wbxml.PageCalendar, "MeetingStatus", wbxml.Text(itoa(meetingStatus))))
	// OrganizerEmail (0x19) / OrganizerName (0x1A). MS-ASAIRS requires
	// OrganizerEmail on an item with attendees; Exchange rejects a meeting
	// Add without it with item status 6 (ConversionError). Only meaningful
	// for meetings, so emit alongside MeetingStatus 1.
	if meetingStatus == 1 && draft.OrganizerEmail != "" {
		app.Children = append(app.Children,
			wbxml.E(wbxml.PageCalendar, "OrganizerEmail", wbxml.Text(draft.OrganizerEmail)))
		if draft.OrganizerName != "" {
			app.Children = append(app.Children,
				wbxml.E(wbxml.PageCalendar, "OrganizerName", wbxml.Text(draft.OrganizerName)))
		}
	}
	// Recurrence (0x1B)
	if rec := recurrenceToWBXML(draft.Recurrence); rec != nil {
		app.Children = append(app.Children, rec)
	}
	// Reminder (0x24), Sensitivity (0x25). A cleared reminder is sent as an
	// explicit 0 so the server drops the stored reminder.
	if draft.Reminder > 0 || draft.clears("reminder") {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Reminder", wbxml.Text(itoa(draft.Reminder))))
	}
	app.Children = append(app.Children,
		wbxml.E(wbxml.PageCalendar, "Sensitivity", wbxml.Text(itoa(draft.Sensitivity))))
	// Subject (0x26), StartTime (0x27)
	if draft.Subject != "" {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text(draft.Subject)))
	}
	if !draft.StartTime.IsZero() {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "StartTime", wbxml.Text(formatEASTime(draft.StartTime))))
	}
	// UID (0x28) — EAS requires a GUID-shaped UID for new events. On
	// update the caller carries the server's existing UID so the item
	// keeps its identity; only a fresh create generates a new one.
	uid := draft.UID
	if uid == "" {
		uid = newEventUID()
	}
	app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "UID", wbxml.Text(uid)))
	// ResponseRequested (0x34) — sent by every major EAS client on Add.
	// ClientUid (0x3C) is deliberately not sent: it was introduced after
	// EAS 14.1 and Exchange rejects it on calendar Add/Change with item
	// status 6 (ConversionError).
	responseRequested := "0"
	if meetingStatus == 1 {
		responseRequested = "1"
	}
	app.Children = append(app.Children,
		wbxml.E(wbxml.PageCalendar, "ResponseRequested", wbxml.Text(responseRequested)))
	// AirSyncBase Body — separate codepage, after the calendar content.
	if draft.Body != "" {
		app.Children = append(app.Children,
			wbxml.E(wbxml.PageAirSyncBase, "Body",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
				wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text(draft.Body)),
			),
		)
	}
	return app
}

func formatEASTime(t time.Time) string {
	// Calendar item datetimes must use the iCalendar compact form
	// "YYYYMMDDTHHMMSSZ" — the same form Exchange itself emits in Sync
	// responses. The dashed "YYYY-MM-DDTHH:MM:SSZ" form from MS-ASAIRS
	// examples parses on mail items but makes Exchange reject a calendar
	// Add/Change with item status 6 (ConversionError). Z-Push accepts
	// both forms.
	return t.UTC().Format("20060102T150405Z")
}

func newEventUID() string {
	// RFC 4122 v4 random GUID. Outlook/EAS clients use GUIDs for calendar
	// UIDs; Exchange rejects non-GUID-shaped UIDs on Add with item
	// status 6 (ConversionError).
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	buf[6] = (buf[6] & 0x0F) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3F) | 0x80 // variant 1
	h := hex.EncodeToString(buf)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

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

func TestSyncEmailBootstrapContainsOnlyKeyAndCollectionID(t *testing.T) {
	request, client := captureInitialSync(t)
	_, err := client.SyncEmail(context.Background(), "5", EmailSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	assertMinimalBootstrapCollection(t, *request)
}

func TestSyncCalendarBootstrapContainsOnlyKeyAndCollectionID(t *testing.T) {
	request, client := captureInitialSync(t)
	_, err := client.SyncCalendar(context.Background(), "calendar", CalendarSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	assertMinimalBootstrapCollection(t, *request)
}

func TestSyncEmailSteadyStateEmptyResponseUsesOneRequest(t *testing.T) {
	state := NewMemoryState()
	if err := state.SetSyncKey(context.Background(), "5", "existing-key"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := newSyncTestClient(t, state, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		writeSyncSuccess(t, w, "5", "next-key")
	})
	if _, err := client.SyncEmail(context.Background(), "5", EmailSyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want 1", calls)
	}
}

func TestSyncCalendarSteadyStateEmptyResponseUsesOneRequest(t *testing.T) {
	state := NewMemoryState()
	if err := state.SetSyncKey(context.Background(), "calendar", "existing-key"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := newSyncTestClient(t, state, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		writeSyncSuccess(t, w, "calendar", "next-key")
	})
	if _, err := client.SyncCalendar(context.Background(), "calendar", CalendarSyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want 1", calls)
	}
}

func TestSyncEmailBootstrapPropagatesFetchError(t *testing.T) {
	calls := 0
	client := newSyncTestClient(t, NewMemoryState(), func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			writeSyncSuccess(t, w, "5", "next-key")
			return
		}
		http.Error(w, "fetch failed", http.StatusInternalServerError)
	})
	if _, err := client.SyncEmail(context.Background(), "5", EmailSyncOptions{}); err == nil {
		t.Fatal("expected second-request error")
	}
}

func TestSyncCalendarBootstrapPropagatesFetchError(t *testing.T) {
	calls := 0
	client := newSyncTestClient(t, NewMemoryState(), func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			writeSyncSuccess(t, w, "calendar", "next-key")
			return
		}
		http.Error(w, "fetch failed", http.StatusInternalServerError)
	})
	if _, err := client.SyncCalendar(context.Background(), "calendar", CalendarSyncOptions{}); err == nil {
		t.Fatal("expected second-request error")
	}
}

func TestSyncCalendarPersistsKeyOnFailedStatus(t *testing.T) {
	state := NewMemoryState()
	if err := state.SetSyncKey(context.Background(), "calendar", "existing-key"); err != nil {
		t.Fatal(err)
	}
	client := newSyncTestClient(t, state, func(w http.ResponseWriter, _ *http.Request) {
		response := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("server-offered-key")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("calendar")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("4")))))}
		encoded, err := wbxml.Marshal(response, wbxml.DefaultRegistry())
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		_, _ = w.Write(encoded)
	})
	_, err := client.SyncCalendar(context.Background(), "calendar", CalendarSyncOptions{})
	if !IsStatusCode(err, 4) {
		t.Fatalf("err = %v, want status 4", err)
	}
	if got, _ := state.SyncKey(context.Background(), "calendar"); got != "server-offered-key" {
		t.Errorf("stored key = %q, want server-offered-key (failed sync must not strand server state)", got)
	}
}

func TestCreateEventSurfacesItemStatus(t *testing.T) {
	state := NewMemoryState()
	if err := state.SetSyncKey(context.Background(), "calendar", "existing-key"); err != nil {
		t.Fatal(err)
	}
	client := newSyncTestClient(t, state, func(w http.ResponseWriter, r *http.Request) {
		// Echo the request's ClientId, Collection Status=1, but the Add
		// itself failed (status 6) with no ServerId — the shape Exchange
		// uses to reject a calendar create.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		req, err := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
		if err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		cid := req.Root.Find("ClientId")
		if cid == nil {
			t.Error("request missing ClientId")
			return
		}
		response := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("next-key")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("calendar")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageAirSync, "Responses",
						wbxml.E(wbxml.PageAirSync, "Add",
							wbxml.E(wbxml.PageAirSync, "ClientId", wbxml.Text(cid.TextContent())),
							wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("6")))))))}
		encoded, err := wbxml.Marshal(response, wbxml.DefaultRegistry())
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		_, _ = w.Write(encoded)
	})
	start := time.Now()
	_, err := client.CreateEvent(context.Background(), "calendar", EventDraft{
		StartTime: start, EndTime: start.Add(time.Hour), Subject: "x",
	})
	if !IsStatusCode(err, 6) {
		t.Fatalf("err = %v, want status 6 (per-item Add failure must not look like success)", err)
	}
}

func newSyncTestClient(t *testing.T, state StateStore, handler http.HandlerFunc) Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{
		ServerURL: server.URL, Username: "user", Password: "password",
		DeviceID: "0123456789abcdef0123456789abcdef", DeviceType: "MCP",
		ASVersion: "14.1", State: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeSyncSuccess(t *testing.T, w http.ResponseWriter, folderID, key string) {
	t.Helper()
	response := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
		wbxml.E(wbxml.PageAirSync, "Collections",
			wbxml.E(wbxml.PageAirSync, "Collection",
				wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
				wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
				wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")))))}
	encoded, err := wbxml.Marshal(response, wbxml.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	_, _ = w.Write(encoded)
}

func captureInitialSync(t *testing.T) (**wbxml.Document, Client) {
	t.Helper()
	var request *wbxml.Document
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		request, err = wbxml.Unmarshal(body, wbxml.DefaultRegistry())
		if err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		response := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("next-key")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("5")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")))))}
		encoded, err := wbxml.Marshal(response, wbxml.DefaultRegistry())
		if err != nil {
			t.Errorf("encode response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{
		ServerURL: server.URL, Username: "user", Password: "password",
		DeviceID: "0123456789abcdef0123456789abcdef", DeviceType: "MCP",
		ASVersion: "14.1", State: NewMemoryState(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &request, client
}

func assertMinimalBootstrapCollection(t *testing.T, request *wbxml.Document) {
	t.Helper()
	if request == nil || request.Root == nil {
		t.Fatal("missing Sync request")
	}
	collection := request.Root.Find("Collection")
	if collection == nil {
		t.Fatal("missing Collection")
	}
	for _, name := range []string{"DeletesAsMoves", "GetChanges", "WindowSize", "Options"} {
		if collection.Find(name) != nil {
			t.Errorf("bootstrap request unexpectedly contains %s", name)
		}
	}
	if key := collection.Find("SyncKey"); key == nil || key.TextContent() != "0" {
		t.Errorf("SyncKey = %v, want 0", key)
	}
	if id := collection.Find("CollectionId"); id == nil {
		t.Error("bootstrap request missing CollectionId")
	}
}

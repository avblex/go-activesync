package eas

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestProvisionDeviceInformationOnlyInInitialRequest(t *testing.T) {
	var requests []*wbxml.Document
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := wbxml.Unmarshal(mustReadAll(t, r), wbxml.DefaultRegistry())
		if err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, doc)
		key := "temporary"
		if len(requests) == 2 {
			key = "final"
		}
		response := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
			wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageProvision, "Policies",
				wbxml.E(wbxml.PageProvision, "Policy",
					wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("MS-EAS-Provisioning-WBXML")),
					wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text(key)))))}
		body, err := wbxml.Marshal(response, wbxml.DefaultRegistry())
		if err != nil {
			t.Errorf("encode response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		ServerURL: server.URL, Username: "user", Password: "password",
		DeviceID: "0123456789abcdef0123456789abcdef", DeviceType: "MCP",
		ASVersion: "14.1", UserAgent: "activesync-mcp/0.1", State: NewMemoryState(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	info := requests[0].Root.Find("DeviceInformation")
	if info == nil || info.Codepage != wbxml.PageSettings {
		t.Fatal("initial request missing Settings/DeviceInformation")
	}
	set := info.Find("Set")
	if set == nil || set.Codepage != wbxml.PageSettings {
		t.Fatal("DeviceInformation missing Settings/Set")
	}
	model := set.Find("Model")
	if model == nil || model.Codepage != wbxml.PageSettings || model.TextContent() != "MCP" {
		t.Fatalf("Model = %#v, want Settings/Model MCP", model)
	}
	if requests[1].Root.Find("DeviceInformation") != nil {
		t.Fatal("acknowledgement request must not contain DeviceInformation")
	}
}

func TestProvisionDeviceInformationProtocolVersions(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"12.1", false},
		{"14.0", false},
		{"14.1", true},
		{"16.0", true},
		{"16.1", true},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			if got := supportsProvisionDeviceInformation(test.version); got != test.want {
				t.Fatalf("supportsProvisionDeviceInformation(%q) = %v, want %v", test.version, got, test.want)
			}
		})
	}
}

func mustReadAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	return body
}

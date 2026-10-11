package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagementStatusRouteIsAuthenticatedAndReadOnly(t *testing.T) {
	p, errBuild := buildPlugin([]byte("enabled: true\nmax_sessions: 32\nmax_pending: 64\n"))
	if errBuild != nil {
		t.Fatal(errBuild)
	}

	registration, errRegister := p.RegisterManagement(context.Background(), pluginapi.ManagementRegistrationRequest{})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	if len(registration.Routes) != 1 || len(registration.Resources) != 0 {
		t.Fatalf("management registration = %#v", registration)
	}
	route := registration.Routes[0]
	if route.Method != http.MethodGet || route.Path != "/plugins/codex-switch-safe/status" || route.Handler != p {
		t.Fatalf("management route = %#v", route)
	}
}

func TestManagementStatusReportsProtectionCountersAndStateWithoutIdentifiers(t *testing.T) {
	p, errBuild := buildPlugin([]byte("enabled: true\ncompaction_policy: strip\nmax_sessions: 32\nmax_pending: 64\n"))
	if errBuild != nil {
		t.Fatal(errBuild)
	}

	seed := codexRequest("seed", "session-private", "thread-private", "auth-private-a", "gpt-private", `{"input":[{"type":"message","role":"user","content":"start"}]}`)
	if _, errIntercept := p.InterceptRequestAfterAuth(context.Background(), seed); errIntercept != nil {
		t.Fatal(errIntercept)
	}
	if errComplete := p.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded}); errComplete != nil {
		t.Fatal(errComplete)
	}

	changed := codexRequest("changed", "session-private", "thread-private", "auth-private-b", "gpt-private", `{"previous_response_id":"response-private","input":[{"type":"reasoning","id":"reasoning-private","encrypted_content":"ciphertext-private"},{"type":"message","role":"user","content":"continue"}]}`)
	response, errIntercept := p.InterceptRequestAfterAuth(context.Background(), changed)
	if errIntercept != nil || len(response.Body) == 0 {
		t.Fatalf("changed-route response = %#v, err=%v", response, errIntercept)
	}

	statusResponse, errStatus := p.HandleManagement(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/codex-switch-safe/status",
	})
	if errStatus != nil {
		t.Fatal(errStatus)
	}
	if statusResponse.StatusCode != http.StatusOK || statusResponse.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("status response = %#v", statusResponse)
	}
	var status managementStatus
	if errDecode := json.Unmarshal(statusResponse.Body, &status); errDecode != nil {
		t.Fatal(errDecode)
	}
	if status.SchemaVersion != 1 || status.Plugin.ID != pluginID || status.Plugin.Version != pluginVersion {
		t.Fatalf("status identity = %#v", status)
	}
	if status.Config.CompactionPolicy != string(compactionPolicyStrip) || status.Config.MaxSessions != 32 || status.Config.MaxPending != 64 {
		t.Fatalf("status config = %#v", status.Config)
	}
	if status.Counters.RequestsObserved != 2 || status.Counters.PassedClean != 1 || status.Counters.RouteStateStripped != 1 || status.Counters.CompletedSucceeded != 1 {
		t.Fatalf("status counters = %#v", status.Counters)
	}
	if status.State.Sessions != 1 || status.State.Pending != 1 {
		t.Fatalf("status state = %#v", status.State)
	}
	privateValues := []string{"session-private", "thread-private", "auth-private", "response-private", "reasoning-private", "ciphertext-private"}
	for _, value := range privateValues {
		if bytes.Contains(statusResponse.Body, []byte(value)) {
			t.Fatalf("status leaked %q: %s", value, statusResponse.Body)
		}
	}
}

func TestManagementStatusRejectsNonGET(t *testing.T) {
	p, errBuild := buildPlugin(nil)
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	response, errHandle := p.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodPost})
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	if response.StatusCode != http.StatusMethodNotAllowed || response.Headers.Get("Allow") != http.MethodGet {
		t.Fatalf("response = %#v", response)
	}
}

func TestManagementStatusReportsPersistenceHealthWithoutPath(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "route-state-v1.json")
	config := fmt.Sprintf("enabled: true\npersistence_enabled: true\nstate_file: %q\n", filepath.ToSlash(stateFile))
	p, errBuild := buildPlugin([]byte(config))
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	defer p.state.stopPersistence()

	response, errStatus := p.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: http.MethodGet})
	if errStatus != nil {
		t.Fatal(errStatus)
	}
	var status managementStatus
	if errDecode := json.Unmarshal(response.Body, &status); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !status.Config.PersistenceEnabled || !status.Persistence.Enabled || !status.Persistence.Healthy {
		t.Fatalf("persistence status = %#v", status.Persistence)
	}
	if bytes.Contains(response.Body, []byte(stateFile)) || bytes.Contains(response.Body, []byte(filepath.ToSlash(stateFile))) {
		t.Fatalf("status leaked state file path: %s", response.Body)
	}
}

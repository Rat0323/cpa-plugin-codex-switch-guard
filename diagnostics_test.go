package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type capturedDiagnostic struct {
	Level   string
	Message string
	Fields  map[string]any
}

type diagnosticCapture struct {
	mu      sync.Mutex
	records []capturedDiagnostic
}

func (c *diagnosticCapture) sink(level, message string, fields map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, capturedDiagnostic{
		Level:   level,
		Message: message,
		Fields:  cloneDiagnosticFields(fields),
	})
}

func (c *diagnosticCapture) snapshot() []capturedDiagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]capturedDiagnostic, len(c.records))
	copy(result, c.records)
	return result
}

func TestParsePluginConfigDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want diagnosticsLevel
	}{
		{name: "default", want: diagnosticsActions},
		{name: "off", raw: "diagnostics: off\n", want: diagnosticsOff},
		{name: "actions", raw: "diagnostics: actions\n", want: diagnosticsActions},
		{name: "debug normalized", raw: "diagnostics: DEBUG\n", want: diagnosticsDebug},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, errParse := parsePluginConfig([]byte(test.raw))
			if errParse != nil {
				t.Fatal(errParse)
			}
			if cfg.Diagnostics != test.want {
				t.Fatalf("Diagnostics = %q, want %q", cfg.Diagnostics, test.want)
			}
		})
	}

	if _, errParse := parsePluginConfig([]byte("diagnostics: verbose\n")); errParse == nil {
		t.Fatal("invalid diagnostics value was accepted")
	}
}

func TestDiagnosticsActionsLogProtectionAndCompletionWithoutSensitiveValues(t *testing.T) {
	p, errBuild := buildPlugin([]byte("enabled: true\npersistence_enabled: false\ndiagnostics: actions\ncompaction_policy: strip\n"))
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	capture := &diagnosticCapture{}
	p.diagnostics = newDiagnosticReporter(diagnosticsActions, 16, capture.sink)

	seed := codexRequest("seed", "session-secret", "thread-secret", "auth-secret-a", "gpt-5.6", `{"input":[{"type":"message","role":"user","content":"start"}]}`)
	if _, errIntercept := p.InterceptRequestAfterAuth(nil, seed); errIntercept != nil {
		t.Fatal(errIntercept)
	}
	p.HandleRequestComplete(nil, pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded})
	if records := capture.snapshot(); len(records) != 0 {
		t.Fatalf("actions mode logged clean pass-through: %#v", records)
	}

	changed := codexRequest("switch", "session-secret", "thread-secret", "auth-secret-b", "gpt-5.6", `{"previous_response_id":"response-secret","input":[{"type":"reasoning","encrypted_content":"cipher-secret"},{"type":"compaction","encrypted_content":"compact-secret"}]}`)
	resp, errIntercept := p.InterceptRequestAfterAuth(nil, changed)
	if errIntercept != nil || resp.Terminate || len(resp.Body) == 0 {
		t.Fatalf("changed response = %#v, err=%v", resp, errIntercept)
	}
	records := capture.snapshot()
	if len(records) != 1 {
		t.Fatalf("action records = %d, want 1: %#v", len(records), records)
	}
	action := records[0]
	if !strings.Contains(action.Message, "action=strip_route_state") ||
		!strings.Contains(action.Message, "reasoning_removed=1") ||
		!strings.Contains(action.Message, "previous_response_id_removed=true") ||
		action.Fields["action"] != "strip_route_state" {
		t.Fatalf("action record = %#v", action)
	}
	if action.Fields["route_relation"] != "changed" || action.Fields["reasoning_removed"] != 1 || action.Fields["compaction_removed"] != 1 || action.Fields["previous_response_id_removed"] != true {
		t.Fatalf("action fields = %#v", action.Fields)
	}
	for _, key := range []string{"session_ref", "route_ref"} {
		value, _ := action.Fields[key].(string)
		if len(value) != 12 {
			t.Fatalf("%s = %q, want 12-character digest reference", key, value)
		}
	}
	assertDiagnosticDoesNotContain(t, records, "auth-secret-a", "auth-secret-b", "session-secret", "thread-secret", "response-secret", "cipher-secret", "compact-secret")

	started := time.Date(2026, time.August, 15, 9, 0, 0, 0, time.UTC)
	p.HandleRequestComplete(nil, pluginapi.RequestCompletion{
		RequestID:   "switch",
		Outcome:     pluginapi.RequestCompletionSucceeded,
		StatusCode:  200,
		StartedAt:   started,
		CompletedAt: started.Add(1250 * time.Millisecond),
	})
	records = capture.snapshot()
	if len(records) != 2 {
		t.Fatalf("records after completion = %d, want 2: %#v", len(records), records)
	}
	completion := records[1]
	if completion.Fields["action"] != "action_complete" || completion.Fields["protected_action"] != "strip_route_state" || completion.Fields["outcome"] != "succeeded" || completion.Fields["status_code"] != 200 || completion.Fields["duration_ms"] != int64(1250) {
		t.Fatalf("completion fields = %#v", completion.Fields)
	}
	if !strings.Contains(completion.Message, "action=action_complete") ||
		!strings.Contains(completion.Message, "protected_action=strip_route_state") ||
		!strings.Contains(completion.Message, "outcome=succeeded") ||
		!strings.Contains(completion.Message, "status_code=200") ||
		!strings.Contains(completion.Message, "duration_ms=1250") {
		t.Fatalf("completion message = %q", completion.Message)
	}
	assertDiagnosticDoesNotContain(t, records, "auth-secret-a", "auth-secret-b", "session-secret", "thread-secret", "response-secret", "cipher-secret", "compact-secret")
}

func TestDiagnosticMessageUsesOnlySafeVisibleFields(t *testing.T) {
	message := diagnosticMessage(map[string]any{
		"action":                       "strip_route_state",
		"reasoning_removed":            1,
		"previous_response_id_removed": true,
		"auth":                         "auth-secret",
		"session_ref":                  "session-ref",
		"route_ref":                    "route-ref",
	})
	for _, want := range []string{
		"codex-switch-guard diagnostic",
		"action=strip_route_state",
		"reasoning_removed=1",
		"previous_response_id_removed=true",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q missing %q", message, want)
		}
	}
	for _, forbidden := range []string{"auth-secret", "session-ref", "route-ref"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("message exposes %q: %q", forbidden, message)
		}
	}
}

func TestDiagnosticsCompletionUsesLatestRetryAction(t *testing.T) {
	capture := &diagnosticCapture{}
	reporter := newDiagnosticReporter(diagnosticsActions, 16, capture.sink)
	req := pluginapi.RequestInterceptRequest{RequestID: "retry"}

	reporter.beginRequest(req.RequestID)
	reporter.action("info", "strip_route_state", req, map[string]any{"session_ref": "first-ref"})
	reporter.beginRequest(req.RequestID)
	reporter.action("warn", "block_route_state", req, map[string]any{
		"session_ref":                  "second-ref",
		"reasoning_blocked":            1,
		"compaction_blocked":           0,
		"previous_response_id_blocked": true,
	})
	reporter.complete(pluginapi.RequestCompletion{RequestID: "retry", Outcome: pluginapi.RequestCompletionRejected, StatusCode: 409})

	records := capture.snapshot()
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3: %#v", len(records), records)
	}
	completion := records[2]
	if completion.Fields["protected_action"] != "block_route_state" || completion.Fields["session_ref"] != "second-ref" {
		t.Fatalf("completion did not use latest retry action: %#v", completion.Fields)
	}
	if completion.Fields["reasoning_blocked"] != 1 || completion.Fields["previous_response_id_blocked"] != true {
		t.Fatalf("completion lost block impact fields: %#v", completion.Fields)
	}
}

func TestDiagnosticsModesControlPassThroughLogging(t *testing.T) {
	tests := []struct {
		name diagnosticsLevel
		want int
	}{
		{name: diagnosticsOff, want: 0},
		{name: diagnosticsActions, want: 0},
		{name: diagnosticsDebug, want: 1},
	}
	for _, test := range tests {
		t.Run(string(test.name), func(t *testing.T) {
			p, errBuild := buildPlugin([]byte("enabled: true\npersistence_enabled: false\n"))
			if errBuild != nil {
				t.Fatal(errBuild)
			}
			capture := &diagnosticCapture{}
			p.diagnostics = newDiagnosticReporter(test.name, 16, capture.sink)
			request := codexRequest("clean", "session", "thread", "auth", "gpt-5.6", `{"input":[{"type":"message","role":"user","content":"hello"}]}`)
			if _, errIntercept := p.InterceptRequestAfterAuth(nil, request); errIntercept != nil {
				t.Fatal(errIntercept)
			}
			if got := len(capture.snapshot()); got != test.want {
				t.Fatalf("records = %d, want %d", got, test.want)
			}
		})
	}
}

func assertDiagnosticDoesNotContain(t *testing.T, records []capturedDiagnostic, secrets ...string) {
	t.Helper()
	raw, errMarshal := json.Marshal(records)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, secret := range secrets {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("diagnostics contain sensitive value %q: %s", secret, raw)
		}
	}
}

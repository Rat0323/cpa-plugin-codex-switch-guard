package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// MockCodexUpstream simulates OpenAI/Codex backend decryption enforcement
type MockCodexUpstream struct{}

type mockResponse struct {
	Status int
	Body   string
}

func (m *MockCodexUpstream) HandleRequest(authID string, bodyBytes []byte) mockResponse {
	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return mockResponse{Status: 400, Body: `{"error":{"message":"Malformed JSON"}}`}
	}

	// 1. Check previous_response_id decryption
	if prevID, ok := payload["previous_response_id"].(string); ok && prevID != "" {
		if !strings.HasPrefix(prevID, "resp_"+authID) {
			return mockResponse{
				Status: 400,
				Body: fmt.Sprintf(
					`{"error":{"type":"invalid_request_error","code":"invalid_previous_response_id","message":"Response '%s' cannot be decrypted or found under credential '%s'"}}`,
					prevID, authID,
				),
			}
		}
	}

	// 2. Check reasoning encrypted_content decryption
	if input, ok := payload["input"].([]any); ok {
		for _, item := range input {
			itemMap, isMap := item.(map[string]any)
			if !isMap {
				continue
			}
			itemType, _ := itemMap["type"].(string)
			encContent, _ := itemMap["encrypted_content"].(string)

			if itemType == "reasoning" && encContent != "" {
				// Encrypted content was sealed by a specific auth key
				expectedPrefix := "enc_" + authID + "_"
				if !strings.HasPrefix(encContent, expectedPrefix) {
					return mockResponse{
						Status: 400,
						Body: fmt.Sprintf(
							`{"error":{"type":"invalid_request_error","code":"invalid_encrypted_reasoning","message":"Encrypted reasoning '%s' failed signature/key decryption on upstream '%s'"}}`,
							encContent, authID,
						),
					}
				}
			}
		}
	}

	// Successful response from upstream
	return mockResponse{
		Status: 200,
		Body: fmt.Sprintf(
			`{"id":"resp_%s_%d","output":[{"type":"reasoning","encrypted_content":"enc_%s_turn_ok"},{"type":"message","content":"Success response from %s"}]}`,
			authID, time.Now().UnixNano(), authID, authID,
		),
	}
}

func TestRigorousReproductionAndProtection(t *testing.T) {
	upstream := &MockCodexUpstream{}
	fmt.Println("\n================================================================================")
	fmt.Println("  CODEX ENCRYPTED REASONING SWITCH SAFETY RIGOROUS VERIFICATION")
	fmt.Println("================================================================================")

	// Step 1: Turn 1 on Credential A
	turn1Input := `{"input":[{"type":"message","role":"user","content":"Explain quantum physics"}]}`
	respTurn1 := upstream.HandleRequest("auth-account-A", []byte(turn1Input))
	if respTurn1.Status != 200 {
		t.Fatalf("Turn 1 failed: %v", respTurn1)
	}
	fmt.Printf("\n[1] Turn 1 on Credential-A -> Status: %d OK\n", respTurn1.Status)

	// Client builds Turn 2 payload using Credential-A's previous_response_id & encrypted reasoning
	turn2Payload := `{
		"previous_response_id": "resp_auth-account-A_1001",
		"input": [
			{
				"type": "reasoning",
				"encrypted_content": "enc_auth-account-A_secret_thought_chain_123"
			},
			{
				"type": "message",
				"role": "user",
				"content": "Give me a simple metaphor"
			}
		]
	}`

	fmt.Println("\n--------------------------------------------------------------------------------")
	fmt.Println("  EXPERIMENT 1: WITHOUT PLUGIN (Directly route Turn 2 to Credential-B)")
	fmt.Println("--------------------------------------------------------------------------------")
	respNoPlugin := upstream.HandleRequest("auth-account-B", []byte(turn2Payload))
	fmt.Printf(">> Client sends Account-A's encrypted reasoning to Account-B without plugin:\n")
	fmt.Printf(">> Result HTTP Status : %d\n", respNoPlugin.Status)
	fmt.Printf(">> Upstream Error Body: %s\n", respNoPlugin.Body)
	if respNoPlugin.Status != 400 {
		t.Fatalf("Expected 400 Bad Request to reproduce error, got %d", respNoPlugin.Status)
	}
	fmt.Printf("💥 [BUG REPRODUCED]: Upstream failed with 400 Bad Request due to mismatched encrypted reasoning!\n")

	fmt.Println("\n--------------------------------------------------------------------------------")
	fmt.Println("  EXPERIMENT 2: WITH PLUGIN (codex-switch-guard enabled)")
	fmt.Println("--------------------------------------------------------------------------------")

	var diagnosticLogs []string
	sink := func(level, msg string, fields map[string]any) {
		logJSON, _ := json.Marshal(fields)
		diagnosticLogs = append(diagnosticLogs, fmt.Sprintf("[%s] %s | fields: %s", strings.ToUpper(level), msg, string(logJSON)))
	}

	cfg := defaultPluginConfig()
	cfg.CompactionPolicy = compactionPolicyStrip
	plugin := &switchGuardPlugin{
		cfg:         cfg,
		state:       newRouteStateStore(cfg),
		diagnostics: newDiagnosticReporter(cfg.Diagnostics, cfg.MaxPending, sink),
	}

	// 2.1 Pass Turn 1 through plugin
	req1 := pluginapi.RequestInterceptRequest{
		RequestID: "req-001",
		ToFormat:  "codex",
		Model:     "gpt-5",
		Headers: http.Header{
			"Session-Id": []string{"conversation-session-xyz"},
		},
		Metadata: map[string]any{"selected_auth_id": "auth-account-A"},
		Body:     []byte(turn1Input),
	}
	_, _ = plugin.InterceptRequestAfterAuth(context.Background(), req1)
	_ = plugin.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{
		RequestID: "req-001",
		Outcome:   pluginapi.RequestCompletionSucceeded,
	})
	fmt.Printf(">> Step 2.1: Turn 1 routed to Account-A registered in plugin state store.\n")

	// 2.2 Turn 2 switched to Account-B through plugin
	req2 := pluginapi.RequestInterceptRequest{
		RequestID: "req-002",
		ToFormat:  "codex",
		Model:     "gpt-5",
		Headers: http.Header{
			"Session-Id": []string{"conversation-session-xyz"},
		},
		Metadata: map[string]any{"selected_auth_id": "auth-account-B"},
		Body:     []byte(turn2Payload),
	}

	respIntercept, errIntercept := plugin.InterceptRequestAfterAuth(context.Background(), req2)
	if errIntercept != nil {
		t.Fatalf("Plugin error: %v", errIntercept)
	}

	fmt.Println("\n>> Step 2.2: Interceptor inspected request during credential switch (A -> B):")
	var prettyCleaned bytes.Buffer
	_ = json.Indent(&prettyCleaned, respIntercept.Body, "   ", "  ")
	fmt.Printf("   Cleaned Body forwarded to upstream:\n   %s\n", prettyCleaned.String())

	// Send cleaned body to Account-B
	respWithPlugin := upstream.HandleRequest("auth-account-B", respIntercept.Body)
	_ = plugin.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{
		RequestID: "req-002",
		Outcome:   pluginapi.RequestCompletionSucceeded,
	})

	fmt.Printf("\n>> Result HTTP Status : %d\n", respWithPlugin.Status)
	fmt.Printf(">> Upstream Response  : %s\n", respWithPlugin.Body)
	if respWithPlugin.Status != 200 {
		t.Fatalf("Expected 200 OK after plugin sanitization, got %d", respWithPlugin.Status)
	}
	fmt.Printf("✅ [PROTECTION SUCCESS]: Plugin successfully stripped foreign encrypted reasoning! Account-B replied 200 OK.\n")

	// 2.3 Turn 3 continues on Account-B (Same Route Preservation Verification)
	fmt.Println("\n--------------------------------------------------------------------------------")
	fmt.Println("  EXPERIMENT 3: SAME ROUTE PRESERVATION (Turn 3 stays on Account-B)")
	fmt.Println("--------------------------------------------------------------------------------")
	turn3Payload := `{
		"previous_response_id": "resp_auth-account-B_2002",
		"input": [
			{
				"type": "reasoning",
				"encrypted_content": "enc_auth-account-B_valid_chain_456"
			},
			{
				"type": "message",
				"role": "user",
				"content": "Tell me more about it"
			}
		]
	}`
	req3 := pluginapi.RequestInterceptRequest{
		RequestID: "req-003",
		ToFormat:  "codex",
		Model:     "gpt-5",
		Headers: http.Header{
			"Session-Id": []string{"conversation-session-xyz"},
		},
		Metadata: map[string]any{"selected_auth_id": "auth-account-B"},
		Body:     []byte(turn3Payload),
	}
	respIntercept3, _ := plugin.InterceptRequestAfterAuth(context.Background(), req3)
	if len(respIntercept3.Body) != 0 {
		t.Fatalf("Expected no stripping on same route, got stripped body: %s", respIntercept3.Body)
	}
	fmt.Printf(">> Plugin recognized same route: Kept request body 100%% byte-for-byte intact (No stripping).\n")
	respWithPluginTurn3 := upstream.HandleRequest("auth-account-B", []byte(turn3Payload))
	_ = plugin.HandleRequestComplete(context.Background(), pluginapi.RequestCompletion{
		RequestID: "req-003",
		Outcome:   pluginapi.RequestCompletionSucceeded,
	})
	if respWithPluginTurn3.Status != 200 {
		t.Fatalf("Expected 200 OK on same route, got %d", respWithPluginTurn3.Status)
	}
	fmt.Printf(">> Result HTTP Status : %d (Same-route reasoning decrypted perfectly)\n", respWithPluginTurn3.Status)

	fmt.Println("\n--------------------------------------------------------------------------------")
	fmt.Println("  DIAGNOSTIC AUDIT LOGS COLLECTED DURING EXPERIMENT")
	fmt.Println("--------------------------------------------------------------------------------")
	for _, l := range diagnosticLogs {
		fmt.Println(l)
	}
	fmt.Println("================================================================================")
}

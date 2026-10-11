package main

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSessionKeyUsesStableCodexConversationNotTurnID(t *testing.T) {
	first := testSessionRequest("session-a", "thread-a", "turn-1", "caller-a")
	second := testSessionRequest("session-a", "thread-a", "turn-2", "caller-a")

	firstInspection, errFirst := inspectPayload(first.Body)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	secondInspection, errSecond := inspectPayload(second.Body)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	firstKey := sessionKeyFromRequest(first, firstInspection)
	secondKey := sessionKeyFromRequest(second, secondInspection)
	if firstKey == "" || secondKey == "" {
		t.Fatalf("session keys = %q, %q", firstKey, secondKey)
	}
	if firstKey != secondKey {
		t.Fatalf("turn changed session key\nfirst=%s\nsecond=%s", firstKey, secondKey)
	}
}

func TestSessionKeySeparatesChildConversationAndCaller(t *testing.T) {
	parent := testSessionRequest("session-parent", "thread-parent", "turn-parent", "caller-a")
	child := testSessionRequest("session-child", "thread-child", "turn-child", "caller-a")
	otherCaller := testSessionRequest("session-parent", "thread-parent", "turn-other", "caller-b")

	parentInspection, _ := inspectPayload(parent.Body)
	childInspection, _ := inspectPayload(child.Body)
	otherInspection, _ := inspectPayload(otherCaller.Body)
	parentKey := sessionKeyFromRequest(parent, parentInspection)
	childKey := sessionKeyFromRequest(child, childInspection)
	otherCallerKey := sessionKeyFromRequest(otherCaller, otherInspection)
	if parentKey == childKey {
		t.Fatalf("child conversation shared parent state key: %s", parentKey)
	}
	if parentKey == otherCallerKey {
		t.Fatalf("caller scope shared state key: %s", parentKey)
	}
}

func TestSessionKeyPrefersExecutionSessionForNestedAgents(t *testing.T) {
	parent := testSessionRequest("shared-session", "shared-thread", "turn-parent", "caller-a")
	child := testSessionRequest("shared-session", "shared-thread", "turn-child", "caller-a")
	parent.Metadata["execution_session_id"] = "execution-parent"
	child.Metadata["execution_session_id"] = "execution-child"

	parentInspection, errParent := inspectPayload(parent.Body)
	if errParent != nil {
		t.Fatal(errParent)
	}
	childInspection, errChild := inspectPayload(child.Body)
	if errChild != nil {
		t.Fatal(errChild)
	}
	parentKey := sessionKeyFromRequest(parent, parentInspection)
	childKey := sessionKeyFromRequest(child, childInspection)
	if parentKey == "" || childKey == "" || parentKey == childKey {
		t.Fatalf("execution session keys = %q, %q", parentKey, childKey)
	}
}

func TestSessionKeyPrefersCanonicalHostIdentity(t *testing.T) {
	first := testSessionRequest("header-a", "thread-a", "turn-a", "caller-a")
	second := testSessionRequest("header-b", "thread-b", "turn-b", "caller-a")
	first.Metadata["canonical_session_id"] = "canonical-session"
	first.Metadata["execution_session_id"] = "execution-a"
	second.Metadata["canonical_session_id"] = "canonical-session"
	second.Metadata["execution_session_id"] = "execution-b"

	firstInspection, errFirst := inspectPayload(first.Body)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	secondInspection, errSecond := inspectPayload(second.Body)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	firstKey := sessionKeyFromRequest(first, firstInspection)
	secondKey := sessionKeyFromRequest(second, secondInspection)
	if firstKey == "" || firstKey != secondKey {
		t.Fatalf("canonical session keys = %q, %q", firstKey, secondKey)
	}
}

func TestSessionKeyNeverUsesAgentMessageContents(t *testing.T) {
	request := testSessionRequest("session-root", "thread-root", "turn-root", "caller-a")
	request.Body = []byte(`{"input":[{"type":"agent_message","author":"/root","recipient":"/root/worker","content":[{"type":"encrypted_content","encrypted_content":"child-private-state"}]}]}`)
	inspection, errInspect := inspectPayload(request.Body)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	key := sessionKeyFromRequest(request, inspection)
	if key == "" {
		t.Fatal("session key = empty")
	}
	if key == opaqueDigest("session-v1", "caller-a", "agent_message", "child-private-state") {
		t.Fatal("session key was derived from nested agent message content")
	}
}

func TestSessionKeyWithoutStableIdentityIsEmpty(t *testing.T) {
	req := pluginapi.RequestInterceptRequest{Body: []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`)}
	inspection, errInspect := inspectPayload(req.Body)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	if key := sessionKeyFromRequest(req, inspection); key != "" {
		t.Fatalf("session key = %q, want empty", key)
	}
}

func TestSessionKeyUsesPromptCacheKeyFallback(t *testing.T) {
	first := pluginapi.RequestInterceptRequest{Body: []byte(`{"prompt_cache_key":"cache-a","window_id":"window-a"}`)}
	second := pluginapi.RequestInterceptRequest{Body: []byte(`{"prompt_cache_key":"cache-a","window_id":"window-b"}`)}
	third := pluginapi.RequestInterceptRequest{Body: []byte(`{"prompt_cache_key":"cache-b","window_id":"window-a"}`)}
	firstInspection, _ := inspectPayload(first.Body)
	secondInspection, _ := inspectPayload(second.Body)
	thirdInspection, _ := inspectPayload(third.Body)
	firstKey := sessionKeyFromRequest(first, firstInspection)
	secondKey := sessionKeyFromRequest(second, secondInspection)
	thirdKey := sessionKeyFromRequest(third, thirdInspection)
	if firstKey == "" || firstKey != secondKey || firstKey == thirdKey {
		t.Fatalf("prompt cache keys = %q, %q, %q", firstKey, secondKey, thirdKey)
	}
}

func TestSessionKeyUsesDirectClientMetadataConversation(t *testing.T) {
	first := pluginapi.RequestInterceptRequest{Body: []byte(`{"client_metadata":{"session_id":"session-a","thread_id":"thread-a"}}`)}
	second := pluginapi.RequestInterceptRequest{Body: []byte(`{"client_metadata":{"session_id":"session-a","thread_id":"thread-a","turn_id":"turn-2"}}`)}
	other := pluginapi.RequestInterceptRequest{Body: []byte(`{"client_metadata":{"session_id":"session-a","thread_id":"thread-b"}}`)}
	firstInspection, _ := inspectPayload(first.Body)
	secondInspection, _ := inspectPayload(second.Body)
	otherInspection, _ := inspectPayload(other.Body)
	firstKey := sessionKeyFromRequest(first, firstInspection)
	secondKey := sessionKeyFromRequest(second, secondInspection)
	otherKey := sessionKeyFromRequest(other, otherInspection)
	if firstKey == "" || firstKey != secondKey || firstKey == otherKey {
		t.Fatalf("client metadata keys = %q, %q, %q", firstKey, secondKey, otherKey)
	}
}

func TestSessionKeyPrefersHeadersOverWeakerPayloadFallbacks(t *testing.T) {
	withConflict := pluginapi.RequestInterceptRequest{
		Headers: http.Header{"Session-Id": []string{"header-session"}},
		Body:    []byte(`{"client_metadata":{"session_id":"body-session","thread_id":"body-thread"},"prompt_cache_key":"body-cache"}`),
	}
	headerOnly := pluginapi.RequestInterceptRequest{
		Headers: http.Header{"Session-Id": []string{"header-session"}},
		Body:    []byte(`{}`),
	}
	conflictInspection, _ := inspectPayload(withConflict.Body)
	headerInspection, _ := inspectPayload(headerOnly.Body)
	conflictKey := sessionKeyFromRequest(withConflict, conflictInspection)
	headerKey := sessionKeyFromRequest(headerOnly, headerInspection)
	if conflictKey == "" || conflictKey != headerKey {
		t.Fatalf("conflicting session keys = %q, %q", conflictKey, headerKey)
	}
}

func TestSessionKeyPrefersNestedCodexMetadataOverDirectClientMetadata(t *testing.T) {
	req := pluginapi.RequestInterceptRequest{Body: []byte(`{"client_metadata":{"session_id":"shared-session","thread_id":"shared-thread","x-codex-turn-metadata":"{\"session_id\":\"nested-session\",\"thread_id\":\"nested-thread\"}"}}`)}
	inspection, errInspect := inspectPayload(req.Body)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	key := sessionKeyFromRequest(req, inspection)
	wantReq := pluginapi.RequestInterceptRequest{Body: []byte(`{"x-codex-turn-metadata":"{\"session_id\":\"nested-session\",\"thread_id\":\"nested-thread\"}"}`)}
	wantInspection, _ := inspectPayload(wantReq.Body)
	want := sessionKeyFromRequest(wantReq, wantInspection)
	if key == "" || key != want {
		t.Fatalf("nested metadata key = %q, want %q", key, want)
	}
}

func testSessionRequest(sessionID, threadID, turnID, callerScope string) pluginapi.RequestInterceptRequest {
	return pluginapi.RequestInterceptRequest{
		Headers: http.Header{
			"Session-Id":            []string{sessionID},
			"Thread-Id":             []string{threadID},
			"X-Codex-Turn-Metadata": []string{`{"session_id":"` + sessionID + `","thread_id":"` + threadID + `","turn_id":"` + turnID + `"}`},
		},
		Body:     []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`),
		Metadata: map[string]any{"caller_scope": callerScope},
	}
}

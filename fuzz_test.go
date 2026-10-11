package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func FuzzInspectAndSanitizePayload(f *testing.F) {
	for _, seed := range []string{
		`{"input":[{"type":"message","role":"user","content":"hello"}]}`,
		`{"previous_response_id":"response-a","input":[{"type":"reasoning","id":"reasoning-a","encrypted_content":"opaque"},{"type":"compaction","encrypted_content":"opaque"},{"type":"message","role":"user","content":"continue"}]}`,
		`{"input":[{"type":"agent_message","content":[{"type":"reasoning","encrypted_content":"nested-must-survive"}]}]}`,
		`{"input":"not-an-array"}`,
		`null`,
		`{`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		inspection, errInspect := inspectPayload(raw)
		if errInspect != nil {
			return
		}
		updated, _, errStrip := inspection.stripRouteBoundStateWithStats()
		if errStrip != nil {
			t.Fatal(errStrip)
		}
		verifyFullySanitizedPayload(t, updated)

		secondInspection, errInspect := inspectPayload(updated)
		if errInspect != nil {
			t.Fatalf("sanitized body is invalid: %v", errInspect)
		}
		_, secondStats, errStrip := secondInspection.stripRouteBoundStateWithStats()
		if errStrip != nil || secondStats.changed() {
			t.Fatalf("full sanitizer is not idempotent: stats=%#v err=%v", secondStats, errStrip)
		}
	})
}

func FuzzSelectiveSanitizer(f *testing.F) {
	for _, seed := range []string{
		`{"input":[{"type":"reasoning","id":"a","encrypted_content":"opaque"},{"type":"reasoning","id":"b","encrypted_content":"opaque"}]}`,
		`{"input":[{"type":"compaction","encrypted_content":"opaque"},{"type":"message","content":"keep"}]}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		inspection, errInspect := inspectPayload(raw)
		if errInspect != nil || len(inspection.Signals.ItemFingerprints) == 0 {
			return
		}
		blocked := append([]string(nil), inspection.Signals.ItemFingerprints...)
		updated, _, errStrip := inspection.stripRetiredRouteBoundItemsWithStats(blocked)
		if errStrip != nil {
			t.Fatal(errStrip)
		}
		after, errInspect := inspectPayload(updated)
		if errInspect != nil {
			t.Fatal(errInspect)
		}
		blockedSet := make(map[string]struct{}, len(blocked))
		for _, fingerprint := range blocked {
			blockedSet[fingerprint] = struct{}{}
		}
		for _, fingerprint := range after.Signals.ItemFingerprints {
			if _, exists := blockedSet[fingerprint]; exists {
				t.Fatalf("blocked fingerprint survived selective sanitization: %q", fingerprint)
			}
		}
	})
}

func FuzzSessionIdentityIsDeterministic(f *testing.F) {
	f.Add("canonical", "session", "thread", "payload-session")
	f.Add("", "", "", "")
	f.Add("\x00invalid", " session ", "thread", "payload")

	f.Fuzz(func(t *testing.T, canonical, session, thread, payloadSession string) {
		body, errMarshal := json.Marshal(map[string]any{
			"session_id": payloadSession,
			"input":      []any{},
		})
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		inspection, errInspect := inspectPayload(body)
		if errInspect != nil {
			t.Fatal(errInspect)
		}
		req := pluginapi.RequestInterceptRequest{
			Headers: http.Header{
				"Session-Id": []string{session},
				"Thread-Id":  []string{thread},
			},
			Metadata: map[string]any{canonicalSessionIDMetadataKey: canonical},
		}
		first := sessionKeyFromRequest(req, inspection)
		second := sessionKeyFromRequest(req, inspection)
		if first != second {
			t.Fatalf("session identity changed: %q != %q", first, second)
		}
		if first != "" {
			decoded, errDecode := hex.DecodeString(first)
			if errDecode != nil || len(decoded) != 32 {
				t.Fatalf("session identity is not an opaque SHA-256 digest: %q", first)
			}
		}
	})
}

func FuzzRouteStatePendingIndexInvariants(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	f.Add([]byte{255, 255, 255})

	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) > 256 {
			operations = operations[:256]
		}
		now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
		cfg := testConfig()
		cfg.MaxPending = 8
		cfg.StateTTL = time.Minute
		store := newRouteStateStoreWithClock(cfg, func() time.Time { return now })
		for step, operation := range operations {
			requestID := fmt.Sprintf("request-%d", operation%8)
			sessionKey := fmt.Sprintf("session-%d", (operation/8)%4)
			route := routeFingerprint{Digest: fmt.Sprintf("route-%d", (operation/32)%3)}
			switch operation % 5 {
			case 0:
				store.prepare(requestID, sessionKey, route, true, stateSignals{})
			case 1:
				store.complete(pluginapi.RequestCompletion{RequestID: requestID, Outcome: pluginapi.RequestCompletionSucceeded})
			case 2:
				store.discardPending(requestID)
			case 3:
				now = now.Add(time.Duration(operation/5+1) * time.Second)
				store.prepare(requestID, sessionKey, route, operation%2 == 0, stateSignals{})
			case 4:
				store.reset()
			}
			assertPendingIndexInvariant(t, store, step)
		}
	})
}

func verifyFullySanitizedPayload(t *testing.T, raw []byte) {
	t.Helper()
	inspection, errInspect := inspectPayload(raw)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	if inspection.Signals.hasRouteBoundState() {
		t.Fatalf("route-bound state survived sanitization: %#v", inspection.Signals)
	}
}

func assertPendingIndexInvariant(t *testing.T, store *routeStateStore, step int) {
	t.Helper()
	expected := make(map[string]map[string]int)
	for _, pending := range store.pending {
		if pending.SessionKey == "" {
			continue
		}
		if expected[pending.SessionKey] == nil {
			expected[pending.SessionKey] = make(map[string]int)
		}
		expected[pending.SessionKey][pendingRouteIndexKey(pending)]++
	}
	if len(expected) != len(store.pendingBySession) {
		t.Fatalf("step %d: indexed sessions=%d, want %d", step, len(store.pendingBySession), len(expected))
	}
	for sessionKey, routes := range expected {
		index := store.pendingBySession[sessionKey]
		if index == nil {
			t.Fatalf("step %d: session %q missing from index", step, sessionKey)
		}
		count := 0
		for routeKey, want := range routes {
			count += want
			if index.RouteCounts[routeKey] != want {
				t.Fatalf("step %d: route count %q/%q=%d, want %d", step, sessionKey, routeKey, index.RouteCounts[routeKey], want)
			}
		}
		if index.Count != count || len(index.RouteCounts) != len(routes) {
			t.Fatalf("step %d: index %q=%#v, want routes %#v", step, sessionKey, index, routes)
		}
	}
}

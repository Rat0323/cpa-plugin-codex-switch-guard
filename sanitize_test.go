package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStripRouteBoundStateOnlyTouchesTopLevelState(t *testing.T) {
	payload := []byte(`{
  "model":"gpt-5.6-terra",
  "previous_response_id":"resp_old",
  "input":[
    {"type":"reasoning","id":"rs_top","encrypted_content":"top-secret"},
    {"type":"compaction","encrypted_content":"compact-secret"},
    {"type":"agent_message","author":"/root/child","recipient":"/root","content":[{"type":"encrypted_content","encrypted_content":"child-secret"}],"internal_chat_message_metadata_passthrough":{"turn_id":"child-turn"}},
    {"type":"function_call_output","call_id":"call_1","output":"done"}
  ],
  "tools":[{"type":"function","name":"spawn_agent"}]
}`)

	inspection, errInspect := inspectPayload(payload)
	if errInspect != nil {
		t.Fatalf("inspectPayload() error = %v", errInspect)
	}
	if !inspection.Signals.HasReasoning || !inspection.Signals.HasCompaction || !inspection.Signals.HasPreviousResponseID {
		t.Fatalf("signals = %#v", inspection.Signals)
	}

	updated, changed, errStrip := inspection.stripRouteBoundState()
	if errStrip != nil {
		t.Fatalf("stripRouteBoundState() error = %v", errStrip)
	}
	if !changed {
		t.Fatal("stripRouteBoundState() changed = false, want true")
	}

	var root map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(updated, &root); errUnmarshal != nil {
		t.Fatalf("decode updated payload: %v", errUnmarshal)
	}
	if _, exists := root["previous_response_id"]; exists {
		t.Fatalf("previous_response_id remained: %s", updated)
	}
	var input []json.RawMessage
	if errUnmarshal := json.Unmarshal(root["input"], &input); errUnmarshal != nil {
		t.Fatalf("decode input: %v", errUnmarshal)
	}
	if len(input) != 2 {
		t.Fatalf("input length = %d, want 2; body=%s", len(input), updated)
	}
	var agent map[string]any
	if errUnmarshal := json.Unmarshal(input[0], &agent); errUnmarshal != nil {
		t.Fatalf("decode agent message: %v", errUnmarshal)
	}
	if got, _ := agent["type"].(string); got != "agent_message" {
		t.Fatalf("input[0].type = %q, want agent_message", got)
	}
	content, ok := agent["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("agent content = %#v", agent["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok || part["type"] != "encrypted_content" || part["encrypted_content"] != "child-secret" {
		t.Fatalf("nested encrypted agent content was changed: %#v", content[0])
	}
	if got, _ := agent["author"].(string); got != "/root/child" {
		t.Fatalf("agent author = %q", got)
	}
	if got, _ := agent["recipient"].(string); got != "/root" {
		t.Fatalf("agent recipient = %q", got)
	}
	if got := string(input[1]); got == "" || !containsJSONType(input[1], "function_call_output") {
		t.Fatalf("function output changed: %s", got)
	}
	if _, exists := root["tools"]; !exists {
		t.Fatalf("tools were removed: %s", updated)
	}
}

func TestStripRouteBoundStateReportsExactStats(t *testing.T) {
	payload := []byte(`{"previous_response_id":"resp","input":[{"type":"reasoning","encrypted_content":"one"},{"type":"reasoning","encrypted_content":"two"},{"type":"compaction","encrypted_content":"three"},{"type":"message","content":"keep"}]}`)
	inspection, errInspect := inspectPayload(payload)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	_, stats, errStrip := inspection.stripRouteBoundStateWithStats()
	if errStrip != nil {
		t.Fatal(errStrip)
	}
	if stats.ReasoningRemoved != 2 || stats.CompactionRemoved != 1 || !stats.PreviousResponseIDRemoved {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestStripRouteBoundStateKeepsCleanPayloadByteForByte(t *testing.T) {
	payload := []byte(`{"model":"gpt-5.6-terra","input":[{"type":"message","role":"user","content":"hello"}]}`)
	inspection, errInspect := inspectPayload(payload)
	if errInspect != nil {
		t.Fatalf("inspectPayload() error = %v", errInspect)
	}
	updated, changed, errStrip := inspection.stripRouteBoundState()
	if errStrip != nil {
		t.Fatalf("stripRouteBoundState() error = %v", errStrip)
	}
	if changed {
		t.Fatal("stripRouteBoundState() changed = true, want false")
	}
	if string(updated) != string(payload) {
		t.Fatalf("clean payload changed\n got: %s\nwant: %s", updated, payload)
	}
}

func TestStripRetiredRouteBoundItemsKeepsCurrentRouteState(t *testing.T) {
	payload := []byte(`{
  "previous_response_id":"resp-current",
  "input":[
    {"type":"reasoning","id":"rs-retired","encrypted_content":"foreign"},
    {"type":"reasoning","id":"rs-current","encrypted_content":"current"},
    {"type":"message","role":"user","content":"continue"}
  ]
}`)
	inspection, errInspect := inspectPayload(payload)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	if len(inspection.Signals.ItemFingerprints) != 2 {
		t.Fatalf("item fingerprints = %d, want 2", len(inspection.Signals.ItemFingerprints))
	}

	updated, changed, errStrip := inspection.stripRetiredRouteBoundItems(inspection.Signals.ItemFingerprints[:1])
	if errStrip != nil || !changed {
		t.Fatalf("stripRetiredRouteBoundItems() changed=%v, err=%v", changed, errStrip)
	}
	if !bytes.Contains(updated, []byte(`"previous_response_id":"resp-current"`)) {
		t.Fatalf("previous_response_id was removed: %s", updated)
	}
	if bytes.Contains(updated, []byte("rs-retired")) {
		t.Fatalf("retired reasoning survived: %s", updated)
	}
	if !bytes.Contains(updated, []byte("rs-current")) || !bytes.Contains(updated, []byte("continue")) {
		t.Fatalf("current-route input was removed: %s", updated)
	}
}

func TestStripRetiredRouteBoundItemsUsesInspectionDigester(t *testing.T) {
	digester := newOpaqueDigester([]byte("01234567890123456789012345678901"))
	payload := []byte(`{"previous_response_id":"resp-current","input":[{"type":"reasoning","id":"rs-retired","encrypted_content":"foreign"},{"type":"message","role":"user","content":"continue"}]}`)
	inspection, errInspect := inspectPayloadWithDigester(payload, digester)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	if len(inspection.Signals.ItemFingerprints) != 1 {
		t.Fatalf("item fingerprints = %d, want 1", len(inspection.Signals.ItemFingerprints))
	}

	updated, stats, errStrip := inspection.stripRetiredRouteBoundItemsWithDigester(digester, inspection.Signals.ItemFingerprints)
	if errStrip != nil || !stats.changed() {
		t.Fatalf("stripRetiredRouteBoundItemsWithDigester() stats=%#v, err=%v", stats, errStrip)
	}
	if bytes.Contains(updated, []byte("rs-retired")) || !bytes.Contains(updated, []byte("continue")) || !bytes.Contains(updated, []byte("resp-current")) {
		t.Fatalf("unexpected sanitized payload: %s", updated)
	}
}

func TestRouteBoundItemFingerprintIsStableAcrossSerialization(t *testing.T) {
	_, first, okFirst := routeBoundInputItem([]byte(`{"type":"reasoning","summary":[],"encrypted_content":"opaque"}`))
	_, second, okSecond := routeBoundInputItem([]byte(`{"encrypted_content":"opaque","summary":[],"type":"reasoning"}`))
	if !okFirst || !okSecond || first == "" || first != second {
		t.Fatalf("fingerprints = %q, %q; ok=%v,%v", first, second, okFirst, okSecond)
	}
}

func TestInspectPayloadAcceptsNonArrayInput(t *testing.T) {
	inspection, errInspect := inspectPayload([]byte(`{"input":"hello","previous_response_id":"resp_old"}`))
	if errInspect != nil {
		t.Fatalf("inspectPayload() error = %v", errInspect)
	}
	if inspection.InputArray {
		t.Fatal("InputArray = true, want false")
	}
	if !inspection.Signals.HasPreviousResponseID {
		t.Fatalf("signals = %#v", inspection.Signals)
	}
}

func containsJSONType(raw json.RawMessage, want string) bool {
	var value struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &value) == nil && value.Type == want
}

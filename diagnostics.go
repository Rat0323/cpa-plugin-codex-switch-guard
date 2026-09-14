package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type diagnosticSink func(level, message string, fields map[string]any)

type diagnosticRecord struct {
	Action     string
	ObservedAt time.Time
	Fields     map[string]any
}

type diagnosticReporter struct {
	mu         sync.Mutex
	level      diagnosticsLevel
	maxPending int
	sink       diagnosticSink
	pending    map[string]diagnosticRecord
}

func newDiagnosticReporter(level diagnosticsLevel, maxPending int, sink diagnosticSink) *diagnosticReporter {
	if sink == nil {
		sink = func(string, string, map[string]any) {}
	}
	return &diagnosticReporter{
		level:      level,
		maxPending: maxPending,
		sink:       sink,
		pending:    make(map[string]diagnosticRecord),
	}
}

func (d *diagnosticReporter) beginRequest(requestID string) {
	if d == nil || d.level == diagnosticsOff {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	d.mu.Lock()
	delete(d.pending, requestID)
	d.mu.Unlock()
}

func (d *diagnosticReporter) action(level, action string, req pluginapi.RequestInterceptRequest, fields map[string]any) {
	if d == nil || d.level == diagnosticsOff {
		return
	}
	fields = diagnosticFields(req, fields)
	fields["action"] = action
	d.sink(level, diagnosticMessage(fields), fields)

	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return
	}
	record := diagnosticRecord{Action: action, ObservedAt: time.Now(), Fields: completionFields(fields)}
	d.mu.Lock()
	d.pending[requestID] = record
	d.trimPendingLocked()
	d.mu.Unlock()
}

func (d *diagnosticReporter) debug(action string, req pluginapi.RequestInterceptRequest, fields map[string]any) {
	if d == nil || d.level != diagnosticsDebug {
		return
	}
	fields = diagnosticFields(req, fields)
	fields["action"] = action
	d.sink("debug", diagnosticMessage(fields), fields)
}

func (d *diagnosticReporter) event(level, action string, req pluginapi.RequestInterceptRequest, fields map[string]any) {
	if d == nil || d.level == diagnosticsOff {
		return
	}
	fields = diagnosticFields(req, fields)
	fields["action"] = action
	d.sink(level, diagnosticMessage(fields), fields)
}

func (d *diagnosticReporter) complete(completion pluginapi.RequestCompletion) {
	if d == nil || d.level == diagnosticsOff {
		return
	}
	requestID := strings.TrimSpace(completion.RequestID)
	if requestID == "" {
		return
	}
	d.mu.Lock()
	record, exists := d.pending[requestID]
	if exists {
		delete(d.pending, requestID)
	}
	d.mu.Unlock()
	if !exists {
		return
	}

	fields := cloneDiagnosticFields(record.Fields)
	fields["action"] = "action_complete"
	fields["protected_action"] = record.Action
	fields["outcome"] = string(completion.Outcome)
	if completion.StatusCode != 0 {
		fields["status_code"] = completion.StatusCode
	}
	if !completion.StartedAt.IsZero() && !completion.CompletedAt.IsZero() {
		fields["duration_ms"] = completion.CompletedAt.Sub(completion.StartedAt).Milliseconds()
	}
	level := "info"
	if completion.Outcome != pluginapi.RequestCompletionSucceeded {
		level = "warn"
	}
	d.sink(level, diagnosticMessage(fields), fields)
}

// CPA's console formatter intentionally renders only a fixed field allowlist.
// Repeat selected safe fields in the message so diagnostics stay useful there.
func diagnosticMessage(fields map[string]any) string {
	parts := []string{pluginID + " diagnostic"}
	for _, key := range []string{
		"action",
		"protected_action",
		"outcome",
		"status_code",
		"duration_ms",
		"route_relation",
		"route_known",
		"has_reasoning",
		"has_compaction",
		"has_previous_response_id",
		"reasoning_removed",
		"compaction_removed",
		"previous_response_id_removed",
		"stream",
		"reason",
	} {
		value, exists := fields[key]
		if !exists {
			continue
		}
		parts = append(parts, key+"="+diagnosticMessageValue(value))
	}
	return strings.Join(parts, " ")
}

func diagnosticMessageValue(value any) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	text = strings.NewReplacer("\r", "_", "\n", "_", "\t", "_").Replace(text)
	return text
}

func (d *diagnosticReporter) trimPendingLocked() {
	limit := d.maxPending
	if limit < 1 {
		limit = defaultMaxPending
	}
	for len(d.pending) > limit {
		var oldestID string
		var oldest time.Time
		for requestID, record := range d.pending {
			if oldestID == "" || record.ObservedAt.Before(oldest) || (record.ObservedAt.Equal(oldest) && requestID < oldestID) {
				oldestID = requestID
				oldest = record.ObservedAt
			}
		}
		delete(d.pending, oldestID)
	}
}

func diagnosticFields(req pluginapi.RequestInterceptRequest, fields map[string]any) map[string]any {
	fields = cloneDiagnosticFields(fields)
	fields["plugin_id"] = pluginID
	if requestID := strings.TrimSpace(req.RequestID); requestID != "" {
		fields["request_id"] = requestID
	}
	if req.Stream {
		fields["stream"] = true
	}
	return fields
}

func completionFields(fields map[string]any) map[string]any {
	kept := make(map[string]any)
	for _, key := range []string{
		"plugin_id", "request_id", "session_ref", "route_ref", "route_relation",
		"route_known", "reasoning_removed", "compaction_removed",
		"previous_response_id_removed", "stream",
	} {
		if value, exists := fields[key]; exists {
			kept[key] = value
		}
	}
	return kept
}

func cloneDiagnosticFields(fields map[string]any) map[string]any {
	cloned := make(map[string]any, len(fields)+4)
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}

func diagnosticRef(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func relationName(relation routeRelation) string {
	switch relation {
	case routeRelationSame:
		return "same"
	case routeRelationChanged:
		return "changed"
	default:
		return "unknown"
	}
}

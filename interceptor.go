package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type switchSafePlugin struct {
	cfg         pluginConfig
	state       *routeStateStore
	diagnostics *diagnosticReporter
	metrics     *pluginMetrics
}

var _ pluginapi.RequestInterceptor = (*switchSafePlugin)(nil)
var _ pluginapi.RequestLifecyclePlugin = (*switchSafePlugin)(nil)

func (p *switchSafePlugin) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	// The selected auth is unavailable before this hook, so changing state here
	// would make retry/failover behavior unsafe.
	return pluginapi.RequestInterceptResponse{}, nil
}

func (p *switchSafePlugin) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if p == nil || !p.cfg.Enabled || !isCodexTarget(req) {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	if p.metrics != nil {
		p.metrics.requestsObserved.Add(1)
	}
	p.diagnostics.beginRequest(req.RequestID)

	inspection, errInspect := p.state.inspectPayload(req.Body)
	if errInspect != nil {
		if likelyContainsRouteBoundState(req.Body) {
			if p.metrics != nil {
				p.metrics.unparseableRejected.Add(1)
			}
			p.diagnostics.action("warn", "reject_unparseable_state", req, map[string]any{
				"reason": "payload_parse_failed",
			})
			return stateResetTermination(http.StatusBadRequest, "Codex route-bound state could not be parsed safely; start a fresh turn."), nil
		}
		if p.metrics != nil {
			p.metrics.unparseablePassed.Add(1)
		}
		p.diagnostics.debug("pass_unparseable_without_route_state", req, nil)
		return pluginapi.RequestInterceptResponse{}, nil
	}

	sessionKey := p.state.sessionKey(req, inspection)
	route, routeKnown := p.state.routeFingerprint(req)
	decision := p.state.prepare(req.RequestID, sessionKey, route, routeKnown, inspection.Signals)
	decisionFields := diagnosticDecisionFields(sessionKey, route, routeKnown, decision, inspection.Signals)
	if decision.Reject {
		if p.metrics != nil {
			p.metrics.compactionsBlocked.Add(1)
		}
		p.diagnostics.action("warn", "block_compaction", req, decisionFields)
		return compactionTermination(), nil
	}
	if !decision.Strip && len(decision.BlockedItemFingerprints) == 0 {
		action := "pass_clean"
		if decision.Relation == routeRelationSame && inspection.Signals.hasRouteBoundState() {
			action = "pass_same_route"
			if p.metrics != nil {
				p.metrics.passedSameRoute.Add(1)
			}
		} else if p.metrics != nil {
			p.metrics.passedClean.Add(1)
		}
		p.diagnostics.debug(action, req, decisionFields)
		return pluginapi.RequestInterceptResponse{}, nil
	}

	var updated []byte
	var stats sanitizeStats
	var errStrip error
	action := "strip_route_state"
	if decision.Strip {
		updated, stats, errStrip = inspection.stripRouteBoundStateWithStats()
	} else {
		action = "strip_retired_items"
		updated, stats, errStrip = inspection.stripRetiredRouteBoundItemsWithStats(decision.BlockedItemFingerprints)
	}
	if errStrip != nil || !stats.changed() {
		if p.metrics != nil {
			p.metrics.sanitizeFailures.Add(1)
		}
		p.state.discardPending(req.RequestID)
		decisionFields["reason"] = "sanitize_failed"
		p.diagnostics.action("error", "reject_sanitize_failure", req, decisionFields)
		return stateResetTermination(http.StatusConflict, "Codex route-bound state could not be reset safely; start a fresh turn."), nil
	}
	if p.metrics != nil {
		if decision.Strip {
			p.metrics.routeStateStripped.Add(1)
		} else {
			p.metrics.retiredItemsStripped.Add(1)
		}
	}
	decisionFields["reasoning_removed"] = stats.ReasoningRemoved
	decisionFields["compaction_removed"] = stats.CompactionRemoved
	decisionFields["previous_response_id_removed"] = stats.PreviousResponseIDRemoved
	p.diagnostics.action("info", action, req, decisionFields)
	return pluginapi.RequestInterceptResponse{Body: updated}, nil
}

func (p *switchSafePlugin) HandleRequestComplete(_ context.Context, completion pluginapi.RequestCompletion) error {
	if p == nil || !p.cfg.Enabled {
		return nil
	}
	tracked := p.state.complete(completion)
	p.diagnostics.complete(completion)
	if tracked {
		p.metrics.recordCompletion(completion.Outcome)
	}
	return nil
}

func diagnosticDecisionFields(sessionKey string, route routeFingerprint, routeKnown bool, decision routeDecision, signals stateSignals) map[string]any {
	fields := map[string]any{
		"route_known":              routeKnown,
		"route_relation":           relationName(decision.Relation),
		"has_reasoning":            signals.HasReasoning,
		"has_compaction":           signals.HasCompaction,
		"has_previous_response_id": signals.HasPreviousResponseID,
	}
	if ref := diagnosticRef(sessionKey); ref != "" {
		fields["session_ref"] = ref
	}
	if ref := diagnosticRef(route.Digest); ref != "" {
		fields["route_ref"] = ref
	}
	return fields
}

func isCodexTarget(req pluginapi.RequestInterceptRequest) bool {
	return strings.EqualFold(strings.TrimSpace(req.ToFormat), "codex")
}

func compactionTermination() pluginapi.RequestInterceptResponse {
	return stateResetTermination(http.StatusConflict, "Codex compaction belongs to a different or unknown upstream credential. Retry on the original credential, or set compaction_policy: strip to continue without compaction.")
}

func stateResetTermination(status int, message string) pluginapi.RequestInterceptResponse {
	body, errMarshal := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "codex_switch_safe_reset_required",
			"message": message,
		},
	})
	if errMarshal != nil {
		body = []byte(`{"error":{"type":"codex_switch_safe_reset_required","message":"Codex route-bound state must be reset."}}`)
	}
	return pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      status,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		ResponseBody:    body,
	}
}

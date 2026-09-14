package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type switchGuardPlugin struct {
	cfg         pluginConfig
	state       *routeStateStore
	diagnostics *diagnosticReporter
	digester    opaqueDigester
}

var _ pluginapi.RequestInterceptor = (*switchGuardPlugin)(nil)
var _ pluginapi.RequestLifecyclePlugin = (*switchGuardPlugin)(nil)

func (p *switchGuardPlugin) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	// The selected auth is unavailable before this hook, so changing state here
	// would make retry/failover behavior unsafe.
	return pluginapi.RequestInterceptResponse{}, nil
}

func (p *switchGuardPlugin) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if p == nil || !p.cfg.Enabled || !isCodexTarget(req) {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	p.diagnostics.beginRequest(req.RequestID)

	inspection, errInspect := inspectPayloadWithDigester(req.Body, p.digester)
	if errInspect != nil {
		if likelyContainsRouteBoundState(req.Body) {
			p.diagnostics.action("warn", "reject_unparseable_state", req, map[string]any{
				"reason": "payload_parse_failed",
			})
			return stateResetTermination(http.StatusBadRequest, "Codex route-bound state could not be parsed safely; start a fresh turn."), nil
		}
		p.diagnostics.debug("pass_unparseable_without_route_state", req, nil)
		return pluginapi.RequestInterceptResponse{}, nil
	}

	sessionKey := sessionKeyFromRequestWithDigester(req, inspection, p.digester)
	route, routeKnown := routeFingerprintFromRequestWithDigester(req, p.digester)
	decision := p.state.prepare(req.RequestID, sessionKey, route, routeKnown, inspection.Signals)
	if errPersist := p.state.takePersistenceError(); errPersist != nil {
		p.diagnostics.event("warn", "state_persistence_degraded", req, map[string]any{"reason": "state_write_failed"})
	}
	decisionFields := diagnosticDecisionFields(sessionKey, route, routeKnown, decision, inspection.Signals)
	if decision.Reject {
		p.diagnostics.action("warn", "block_compaction", req, decisionFields)
		return compactionTermination(), nil
	}
	if !decision.Strip && len(decision.BlockedItemFingerprints) == 0 {
		action := "pass_clean"
		if decision.Relation == routeRelationSame && inspection.Signals.hasRouteBoundState() {
			action = "pass_same_route"
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
		updated, stats, errStrip = inspection.stripRetiredRouteBoundItemsWithDigester(p.digester, decision.BlockedItemFingerprints)
	}
	if errStrip != nil || !stats.changed() {
		p.state.discardPending(req.RequestID)
		decisionFields["reason"] = "sanitize_failed"
		p.diagnostics.action("error", "reject_sanitize_failure", req, decisionFields)
		return stateResetTermination(http.StatusConflict, "Codex route-bound state could not be reset safely; start a fresh turn."), nil
	}
	decisionFields["reasoning_removed"] = stats.ReasoningRemoved
	decisionFields["compaction_removed"] = stats.CompactionRemoved
	decisionFields["previous_response_id_removed"] = stats.PreviousResponseIDRemoved
	p.diagnostics.action("info", action, req, decisionFields)
	return pluginapi.RequestInterceptResponse{Body: updated}, nil
}

func (p *switchGuardPlugin) HandleRequestComplete(_ context.Context, completion pluginapi.RequestCompletion) error {
	if p == nil || !p.cfg.Enabled {
		return nil
	}
	errPersist := p.state.complete(completion)
	p.diagnostics.complete(completion)
	return errPersist
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

const resetRequiredErrorType = "codex_switch_guard_reset_required"

func compactionTermination() pluginapi.RequestInterceptResponse {
	return stateResetTermination(http.StatusConflict, "Codex compaction belongs to a different or unknown upstream credential. Retry on the original credential, or set compaction_policy: strip to continue without compaction.")
}

func stateResetTermination(status int, message string) pluginapi.RequestInterceptResponse {
	body, errMarshal := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    resetRequiredErrorType,
			"message": message,
		},
	})
	if errMarshal != nil {
		body = []byte(`{"error":{"type":"` + resetRequiredErrorType + `","message":"Codex route-bound state must be reset."}}`)
	}
	return pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      status,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		ResponseBody:    body,
	}
}

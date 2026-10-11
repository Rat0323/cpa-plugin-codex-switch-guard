package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var _ pluginapi.Scheduler = (*switchGuardPlugin)(nil)

func (p *switchGuardPlugin) Pick(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
	if p == nil || !p.cfg.Enabled || !p.cfg.RouteAffinity || !schedulerTargetsCodex(req) {
		return pluginapi.SchedulerPickResponse{}, nil
	}

	sessionKey := sessionKeyFromSchedulerRequest(req, p.digester)
	committedRoute, hasRoute := p.state.committedRoute(sessionKey)
	if !hasRoute {
		return pluginapi.SchedulerPickResponse{}, nil
	}
	for _, candidate := range req.Candidates {
		candidateRoute := routeFingerprintFromSchedulerCandidate(req, candidate, p.digester)
		if candidateRoute.equal(committedRoute) {
			return pluginapi.SchedulerPickResponse{
				AuthID:  candidate.ID,
				Handled: true,
			}, nil
		}
	}
	return pluginapi.SchedulerPickResponse{}, nil
}

func schedulerTargetsCodex(req pluginapi.SchedulerPickRequest) bool {
	if strings.EqualFold(strings.TrimSpace(req.Provider), "codex") {
		return true
	}
	for _, candidate := range req.Candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Provider), "codex") {
			return true
		}
	}
	return false
}

func sessionKeyFromSchedulerRequest(req pluginapi.SchedulerPickRequest, digester opaqueDigester) string {
	inspection := payloadInspection{}
	headers := http.Header{}
	for name, values := range req.Options.Headers {
		if name == "" || len(values) == 0 {
			continue
		}
		headers[name] = append(headers[name], values...)
	}
	interceptReq := pluginapi.RequestInterceptRequest{
		Headers:  headers,
		Metadata: req.Options.Metadata,
	}
	return sessionKeyFromRequestWithDigester(interceptReq, inspection, digester)
}

func routeFingerprintFromSchedulerCandidate(req pluginapi.SchedulerPickRequest, candidate pluginapi.SchedulerAuthCandidate, digester opaqueDigester) routeFingerprint {
	authID := normalizeOpaqueIdentifier(candidate.ID)
	model := normalizeOpaqueIdentifier(req.Model)
	toFormat := strings.ToLower(strings.TrimSpace(candidate.Provider))
	if toFormat == "" {
		toFormat = strings.ToLower(strings.TrimSpace(req.Provider))
	}
	if authID == "" || model == "" || toFormat != "codex" {
		return routeFingerprint{}
	}
	return routeFingerprint{Digest: digester.digest("route-v1", authID, model, toFormat)}
}

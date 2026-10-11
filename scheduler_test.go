package main

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSchedulerPreservesCommittedRoute(t *testing.T) {
	cfg := testConfig()
	store := newRouteStateStore(cfg)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{"execution_session_id": "session-a"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-a", Provider: "codex"},
			{ID: "auth-b", Provider: "codex"},
		},
	}
	session := sessionKeyFromSchedulerRequest(req, processOpaqueDigester)
	route := routeFingerprintFromSchedulerCandidate(req, req.Candidates[0], processOpaqueDigester)
	seedCommittedRoute(store, session, route)

	plugin := &switchGuardPlugin{
		cfg:      cfg,
		state:    store,
		digester: processOpaqueDigester,
	}
	resp, errPick := plugin.Pick(context.Background(), req)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if !resp.Handled || resp.AuthID != "auth-a" {
		t.Fatalf("Pick() = %#v, want handled auth-a", resp)
	}
}

func TestSchedulerAbstainsWithoutCommittedRoute(t *testing.T) {
	cfg := testConfig()
	store := newRouteStateStore(cfg)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{"execution_session_id": "session-a"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex"}},
	}
	plugin := &switchGuardPlugin{
		cfg:      cfg,
		state:    store,
		digester: processOpaqueDigester,
	}

	resp, errPick := plugin.Pick(context.Background(), req)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if resp.Handled {
		t.Fatalf("Pick() = %#v, want plugin to abstain", resp)
	}
}

func TestSchedulerAffinityCanBeDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.RouteAffinity = false
	store := newRouteStateStore(cfg)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{"execution_session_id": "session-a"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex"}},
	}
	session := sessionKeyFromSchedulerRequest(req, processOpaqueDigester)
	route := routeFingerprintFromSchedulerCandidate(req, req.Candidates[0], processOpaqueDigester)
	seedCommittedRoute(store, session, route)
	plugin := &switchGuardPlugin{
		cfg:      cfg,
		state:    store,
		digester: processOpaqueDigester,
	}

	resp, errPick := plugin.Pick(context.Background(), req)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if resp.Handled {
		t.Fatalf("Pick() = %#v, want affinity disabled", resp)
	}
}

func TestSchedulerMatchesCodexCandidateInMixedProviderRoute(t *testing.T) {
	cfg := testConfig()
	store := newRouteStateStore(cfg)
	req := pluginapi.SchedulerPickRequest{
		Provider:  "openai",
		Providers: []string{"openai", "codex"},
		Model:     "gpt-5",
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{"execution_session_id": "session-mixed"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-openai", Provider: "openai"},
			{ID: "auth-codex", Provider: "codex"},
		},
	}
	session := sessionKeyFromSchedulerRequest(req, processOpaqueDigester)
	route := routeFingerprintFromSchedulerCandidate(pluginapi.SchedulerPickRequest{
		Provider: "codex", Model: req.Model,
	}, req.Candidates[1], processOpaqueDigester)
	seedCommittedRoute(store, session, route)
	plugin := &switchGuardPlugin{cfg: cfg, state: store, digester: processOpaqueDigester}

	resp, errPick := plugin.Pick(context.Background(), req)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if !resp.Handled || resp.AuthID != "auth-codex" {
		t.Fatalf("Pick() = %#v, want Codex auth-codex", resp)
	}
}

func TestSchedulerNeverMatchesNonCodexCandidate(t *testing.T) {
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Model: "gpt-5"}
	candidate := pluginapi.SchedulerAuthCandidate{ID: "auth-a", Provider: "openai"}
	route := routeFingerprintFromSchedulerCandidate(req, candidate, processOpaqueDigester)
	if route.Digest != "" {
		t.Fatalf("route = %#v, want no Codex fingerprint", route)
	}
}

func TestSchedulerAbstainsWhenCommittedCredentialIsUnavailable(t *testing.T) {
	cfg := testConfig()
	store := newRouteStateStore(cfg)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{"execution_session_id": "session-a"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-b", Provider: "codex", Priority: 10},
		},
	}
	session := sessionKeyFromSchedulerRequest(req, processOpaqueDigester)
	route := routeFingerprintFromSchedulerCandidate(req, pluginapi.SchedulerAuthCandidate{
		ID: "auth-a", Provider: "codex",
	}, processOpaqueDigester)
	seedCommittedRoute(store, session, route)
	plugin := &switchGuardPlugin{cfg: cfg, state: store, digester: processOpaqueDigester}

	resp, errPick := plugin.Pick(context.Background(), req)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if resp.Handled {
		t.Fatalf("Pick() = %#v, want host fallback", resp)
	}
}

func TestSchedulerPreservesLowerPriorityCommittedCredential(t *testing.T) {
	cfg := testConfig()
	store := newRouteStateStore(cfg)
	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5",
		Options: pluginapi.SchedulerOptions{
			Metadata: map[string]any{"execution_session_id": "session-a"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-b", Provider: "codex", Priority: 10},
			{ID: "auth-a", Provider: "codex", Priority: 1},
		},
	}
	session := sessionKeyFromSchedulerRequest(req, processOpaqueDigester)
	route := routeFingerprintFromSchedulerCandidate(req, req.Candidates[1], processOpaqueDigester)
	seedCommittedRoute(store, session, route)
	plugin := &switchGuardPlugin{cfg: cfg, state: store, digester: processOpaqueDigester}

	resp, errPick := plugin.Pick(context.Background(), req)
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if !resp.Handled || resp.AuthID != "auth-a" {
		t.Fatalf("Pick() = %#v, want lower-priority auth-a", resp)
	}
}

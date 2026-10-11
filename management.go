package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const managementStatusPath = "/plugins/" + pluginID + "/status"

type managementStatus struct {
	SchemaVersion int `json:"schema_version"`
	Plugin        struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"plugin"`
	UptimeSeconds int64 `json:"uptime_seconds"`
	Enabled       bool  `json:"enabled"`
	Config        struct {
		CompactionPolicy   string `json:"compaction_policy"`
		PersistenceEnabled bool   `json:"persistence_enabled"`
		StateTTL           string `json:"state_ttl"`
		MaxSessions        int    `json:"max_sessions"`
		MaxPending         int    `json:"max_pending"`
		Diagnostics        string `json:"diagnostics"`
		Priority           int    `json:"priority"`
	} `json:"config"`
	State       routeStateSnapshot        `json:"state"`
	Persistence persistenceStatusSnapshot `json:"persistence"`
	Counters    protectionCounters        `json:"counters"`
}

var _ pluginapi.ManagementAPI = (*switchSafePlugin)(nil)
var _ pluginapi.ManagementHandler = (*switchSafePlugin)(nil)

func (p *switchSafePlugin) RegisterManagement(context.Context, pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{{
			Method:      http.MethodGet,
			Path:        managementStatusPath,
			Description: "Privacy-safe Codex route protection counters and in-memory state summary.",
			Handler:     p,
		}},
	}, nil
}

func (p *switchSafePlugin) HandleManagement(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	if req.Method != "" && req.Method != http.MethodGet {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    http.Header{"Allow": []string{http.MethodGet}},
		}, nil
	}

	status := managementStatus{SchemaVersion: 1}
	status.Plugin.ID = pluginID
	status.Plugin.Name = pluginName
	status.Plugin.Version = pluginVersion
	if p != nil {
		status.Enabled = p.cfg.Enabled
		status.Config.CompactionPolicy = string(p.cfg.CompactionPolicy)
		status.Config.PersistenceEnabled = p.cfg.PersistenceEnabled
		status.Config.StateTTL = p.cfg.StateTTL.String()
		status.Config.MaxSessions = p.cfg.MaxSessions
		status.Config.MaxPending = p.cfg.MaxPending
		status.Config.Diagnostics = string(p.cfg.Diagnostics)
		status.Config.Priority = p.cfg.Priority
		status.State = p.state.snapshot()
		status.Persistence = p.state.persistenceStatus()
		status.Counters = p.metrics.snapshot()
		status.UptimeSeconds = int64(p.metrics.uptime(time.Now()) / time.Second)
	}
	body, errMarshal := json.Marshal(status)
	if errMarshal != nil {
		return pluginapi.ManagementResponse{}, errMarshal
	}
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}, nil
}

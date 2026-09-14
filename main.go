package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var pluginVersion = "0.4.0"

const pluginSchemaVersion = pluginabi.SchemaVersion

func buildPlugin(configYAML []byte) (*routeGuardPlugin, error) {
	cfg, errParse := parsePluginConfig(configYAML)
	if errParse != nil {
		return nil, errParse
	}
	state := newRouteStateStore(cfg)
	if cfg.Enabled && cfg.PersistenceEnabled {
		state = newPersistentRouteStateStore(cfg)
	}
	plugin := &routeGuardPlugin{
		cfg:         cfg,
		state:       state,
		diagnostics: newDiagnosticReporter(cfg.Diagnostics, cfg.MaxPending, hostDiagnosticSink),
		digester:    state.digester,
	}
	if state.persistenceWarning != nil {
		plugin.diagnostics.event("warn", "state_persistence_degraded", pluginapi.RequestInterceptRequest{}, map[string]any{"reason": "state_load_or_write_failed"})
	}
	return plugin, nil
}

func pluginMetadata() pluginapi.Metadata {
	return pluginapi.Metadata{
		Name:             pluginName,
		Version:          pluginVersion,
		Author:           "Rat0323",
		GitHubRepository: "https://github.com/Rat0323/cpa-plugin-codex-context-guard",
		ConfigFields: []pluginapi.ConfigField{
			{
				Name:        "persistence_enabled",
				Type:        pluginapi.ConfigFieldTypeBoolean,
				Description: "Persist opaque route bindings across CPA restarts. Enabled by default; disable only if restart continuity is not required.",
			},
			{
				Name:        "state_file",
				Type:        pluginapi.ConfigFieldTypeString,
				Description: "Optional absolute or relative state-file path. The default is the OS user configuration directory.",
			},
			{
				Name:        "compaction_policy",
				Type:        pluginapi.ConfigFieldTypeEnum,
				EnumValues:  []string{string(compactionPolicyBlock), string(compactionPolicyStrip)},
				Description: "Compaction handling on route changes. block (recommended) rejects unsafe switches; strip drops compaction state and continues.",
			},
			{
				Name:        "state_ttl",
				Type:        pluginapi.ConfigFieldTypeString,
				Description: "How long a conversation route binding stays in memory. Accepts 1m through 24h; default: 4h.",
			},
			{
				Name:        "max_sessions",
				Type:        pluginapi.ConfigFieldTypeInteger,
				Description: "Maximum conversation route entries retained in memory. Usually keep the default: 4096.",
			},
			{
				Name:        "max_pending",
				Type:        pluginapi.ConfigFieldTypeInteger,
				Description: "Maximum in-flight selected-auth attempts tracked. Usually keep the default: 8192.",
			},
			{
				Name:        "diagnostics",
				Type:        pluginapi.ConfigFieldTypeEnum,
				EnumValues:  []string{string(diagnosticsOff), string(diagnosticsActions), string(diagnosticsDebug)},
				Description: "Privacy-safe structured diagnostics. actions (default) logs protection actions and their outcomes; debug also logs safe pass-through decisions; off disables diagnostics.",
			},
		},
	}
}

func main() {}

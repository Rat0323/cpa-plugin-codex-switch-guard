package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var pluginVersion = "0.3.1"

const pluginSchemaVersion = pluginabi.SchemaVersion

func buildPlugin(configYAML []byte) (*switchSafePlugin, error) {
	cfg, errParse := parsePluginConfig(configYAML)
	if errParse != nil {
		return nil, errParse
	}
	return &switchSafePlugin{
		cfg:         cfg,
		state:       newConfiguredRouteStateStore(cfg),
		diagnostics: newDiagnosticReporter(cfg.Diagnostics, cfg.MaxPending, hostDiagnosticSink),
		metrics:     newPluginMetrics(),
	}, nil
}

func pluginMetadata() pluginapi.Metadata {
	return pluginapi.Metadata{
		Name:             pluginName,
		Version:          pluginVersion,
		Author:           "Rat0323",
		GitHubRepository: "https://github.com/Rat0323/cpa-plugin-codex-switch-safe",
		ConfigFields: []pluginapi.ConfigField{
			{
				Name:        "compaction_policy",
				Type:        pluginapi.ConfigFieldTypeEnum,
				EnumValues:  []string{string(compactionPolicyBlock), string(compactionPolicyStrip)},
				Description: "Compaction handling on route changes. block (recommended) rejects unsafe switches; strip drops compaction state and continues.",
			},
			{
				Name:        "persistence_enabled",
				Type:        pluginapi.ConfigFieldTypeBoolean,
				Description: "Keep opaque committed route bindings across CPA restarts. Default: false.",
			},
			{
				Name:        "state_file",
				Type:        pluginapi.ConfigFieldTypeString,
				Description: "Optional absolute path for persisted route state. Default: the user config directory.",
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

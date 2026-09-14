package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	pluginID   = "codex-switch-guard"
	pluginName = "Codex Switch Guard"

	defaultStateTTL    = 4 * time.Hour
	defaultMaxSessions = 4096
	defaultMaxPending  = 8192
	defaultDiagnostics = diagnosticsActions
	maxStateTTL        = 24 * time.Hour
	maxTrackedEntries  = 65536
)

type compactionPolicy string

type diagnosticsLevel string

const (
	compactionPolicyBlock compactionPolicy = "block"
	compactionPolicyStrip compactionPolicy = "strip"

	diagnosticsOff     diagnosticsLevel = "off"
	diagnosticsActions diagnosticsLevel = "actions"
	diagnosticsDebug   diagnosticsLevel = "debug"
)

type pluginConfig struct {
	Enabled             bool   `yaml:"enabled"`
	PersistenceRaw      *bool  `yaml:"persistence_enabled"`
	StateFileRaw        string `yaml:"state_file"`
	StateTTLRaw         string `yaml:"state_ttl"`
	MaxSessions         int    `yaml:"max_sessions"`
	MaxPending          int    `yaml:"max_pending"`
	CompactionPolicyRaw string `yaml:"compaction_policy"`
	DiagnosticsRaw      string `yaml:"diagnostics"`

	StateTTL           time.Duration    `yaml:"-"`
	PersistenceEnabled bool             `yaml:"-"`
	StateFile          string           `yaml:"-"`
	CompactionPolicy   compactionPolicy `yaml:"-"`
	Diagnostics        diagnosticsLevel `yaml:"-"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:            true,
		PersistenceEnabled: true,
		StateTTL:           defaultStateTTL,
		MaxSessions:        defaultMaxSessions,
		MaxPending:         defaultMaxPending,
		CompactionPolicy:   compactionPolicyBlock,
		Diagnostics:        defaultDiagnostics,
	}
}

func parsePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return cfg, fmt.Errorf("invalid %s config: %w", pluginID, errUnmarshal)
		}
	}
	if cfg.PersistenceRaw != nil {
		cfg.PersistenceEnabled = *cfg.PersistenceRaw
	}
	if value := strings.TrimSpace(cfg.StateFileRaw); value != "" {
		absolute, errAbs := filepath.Abs(value)
		if errAbs != nil {
			return cfg, fmt.Errorf("invalid state_file %q: %w", value, errAbs)
		}
		cfg.StateFile = filepath.Clean(absolute)
	}

	if value := strings.TrimSpace(cfg.StateTTLRaw); value != "" {
		parsed, errParse := time.ParseDuration(value)
		if errParse != nil {
			return cfg, fmt.Errorf("invalid state_ttl %q: %w", value, errParse)
		}
		cfg.StateTTL = parsed
	}
	if cfg.StateTTL < time.Minute || cfg.StateTTL > maxStateTTL {
		return cfg, fmt.Errorf("state_ttl must be between %s and %s", time.Minute, maxStateTTL)
	}

	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = defaultMaxSessions
	}
	if cfg.MaxSessions < 1 || cfg.MaxSessions > maxTrackedEntries {
		return cfg, fmt.Errorf("max_sessions must be between 1 and %d", maxTrackedEntries)
	}
	if cfg.MaxPending == 0 {
		cfg.MaxPending = defaultMaxPending
	}
	if cfg.MaxPending < 1 || cfg.MaxPending > maxTrackedEntries {
		return cfg, fmt.Errorf("max_pending must be between 1 and %d", maxTrackedEntries)
	}

	policy := strings.ToLower(strings.TrimSpace(cfg.CompactionPolicyRaw))
	if policy == "" {
		policy = string(compactionPolicyBlock)
	}
	switch compactionPolicy(policy) {
	case compactionPolicyBlock, compactionPolicyStrip:
		cfg.CompactionPolicy = compactionPolicy(policy)
	default:
		return cfg, fmt.Errorf("compaction_policy must be %q or %q", compactionPolicyBlock, compactionPolicyStrip)
	}

	diagnostics := strings.ToLower(strings.TrimSpace(cfg.DiagnosticsRaw))
	if diagnostics == "" {
		diagnostics = string(defaultDiagnostics)
	}
	switch diagnosticsLevel(diagnostics) {
	case diagnosticsOff, diagnosticsActions, diagnosticsDebug:
		cfg.Diagnostics = diagnosticsLevel(diagnostics)
	default:
		return cfg, fmt.Errorf("diagnostics must be %q, %q, or %q", diagnosticsOff, diagnosticsActions, diagnosticsDebug)
	}
	return cfg, nil
}

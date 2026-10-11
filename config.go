package main

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	pluginID   = "codex-switch-safe"
	pluginName = "Codex Switch Safe"

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
	Priority            int    `yaml:"priority"`
	StateTTLRaw         string `yaml:"state_ttl"`
	MaxSessions         int    `yaml:"max_sessions"`
	MaxPending          int    `yaml:"max_pending"`
	CompactionPolicyRaw string `yaml:"compaction_policy"`
	DiagnosticsRaw      string `yaml:"diagnostics"`

	StateTTL         time.Duration    `yaml:"-"`
	CompactionPolicy compactionPolicy `yaml:"-"`
	Diagnostics      diagnosticsLevel `yaml:"-"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:          true,
		StateTTL:         defaultStateTTL,
		MaxSessions:      defaultMaxSessions,
		MaxPending:       defaultMaxPending,
		CompactionPolicy: compactionPolicyBlock,
		Diagnostics:      defaultDiagnostics,
	}
}

func parsePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(bytes.TrimSpace(raw)) > 0 {
		// CPA's plugin store installer writes the marketplace manifest under a
		// host-owned `store` key alongside runtime fields when a plugin is
		// installed from the management UI. Strip it before strict decoding so
		// store installs register, while every other unknown field stays an
		// error so misspelled policy names cannot silently take effect.
		stripped, errStrip := stripCPAStoreManifest(raw)
		if errStrip != nil {
			return cfg, fmt.Errorf("invalid %s config: %w", pluginID, errStrip)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(stripped))
		decoder.KnownFields(true)
		if errUnmarshal := decoder.Decode(&cfg); errUnmarshal != nil {
			return cfg, fmt.Errorf("invalid %s config: %w", pluginID, errUnmarshal)
		}
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

// stripCPAStoreManifest removes the host-owned `store` installation manifest
// from a raw plugin config so strict decoding accepts CPA store installs. The
// manifest carries marketplace metadata and never influences plugin behavior,
// so dropping it is safe. Every other field is left untouched and still
// subject to strict decoding.
func stripCPAStoreManifest(raw []byte) ([]byte, error) {
	var root yaml.Node
	if errDecode := yaml.Unmarshal(raw, &root); errDecode != nil {
		return nil, errDecode
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		// Not a mapping; let the strict decoder produce the canonical error.
		return raw, nil
	}
	mapping := root.Content[0]
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return raw, nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key == nil || key.Value != "store" {
			continue
		}
		// Remove the key/value pair in place by slicing the mapping content.
		mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
		break
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if errEncode := encoder.Encode(&root); errEncode != nil {
		return nil, errEncode
	}
	if errClose := encoder.Close(); errClose != nil {
		return nil, errClose
	}
	return encoded.Bytes(), nil
}

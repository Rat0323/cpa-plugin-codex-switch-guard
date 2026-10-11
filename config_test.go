package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePluginConfigRejectsUnknownFields(t *testing.T) {
	_, errParse := parsePluginConfig([]byte("compaction_polciy: strip\n"))
	if errParse == nil {
		t.Fatal("unknown config field was accepted")
	}
	if !strings.Contains(errParse.Error(), "compaction_polciy") {
		t.Fatalf("error = %q, want unknown field name", errParse)
	}
}

func TestParsePluginConfigAcceptsHostRuntimeFields(t *testing.T) {
	cfg, errParse := parsePluginConfig([]byte("enabled: true\npriority: -10\n"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if !cfg.Enabled || cfg.Priority != -10 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestParsePluginConfigAcceptsCPAStoreManifest(t *testing.T) {
	// CPA's plugin store installer writes the marketplace manifest under the
	// `store` key alongside `enabled` when a plugin is installed from the UI.
	// Strict decoding must still reject misspelled policy fields, but accept
	// this host-owned installation metadata so store installs register.
	raw := []byte("enabled: true\nstore:\n  id: codex-switch-safe\n  version: 0.3.0\n  install:\n    type: github-release\ncompaction_policy: strip\n")
	cfg, errParse := parsePluginConfig(raw)
	if errParse != nil {
		t.Fatalf("parsePluginConfig() error = %v", errParse)
	}
	if !cfg.Enabled || cfg.CompactionPolicy != compactionPolicyStrip {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestParsePluginConfigRejectsUnknownFieldAlongsideStore(t *testing.T) {
	// A misspelled policy field must still be rejected even when the CPA store
	// manifest is present, so silent configuration drift is not masked.
	_, errParse := parsePluginConfig([]byte("enabled: true\nstore:\n  id: codex-switch-safe\ncompaction_polciy: strip\n"))
	if errParse == nil {
		t.Fatal("unknown config field was accepted alongside store manifest")
	}
	if !strings.Contains(errParse.Error(), "compaction_polciy") {
		t.Fatalf("error = %q, want unknown field name", errParse)
	}
}

func TestParsePluginConfigPersistenceDefaultsOff(t *testing.T) {
	cfg := defaultPluginConfig()
	if cfg.PersistenceEnabled {
		t.Fatal("default persistence_enabled = true, want false")
	}
	if cfg.StateFile != "" {
		t.Fatalf("default state_file = %q, want empty", cfg.StateFile)
	}
}

func TestParsePluginConfigAcceptsAbsoluteStateFile(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "route-state-v1.json")
	raw := []byte(fmt.Sprintf("enabled: true\npersistence_enabled: true\nstate_file: %q\n", filepath.ToSlash(stateFile)))
	cfg, errParse := parsePluginConfig(raw)
	if errParse != nil {
		t.Fatalf("parsePluginConfig() error = %v", errParse)
	}
	if !cfg.PersistenceEnabled || cfg.StateFile != stateFile {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestParsePluginConfigRejectsRelativeStateFile(t *testing.T) {
	_, errParse := parsePluginConfig([]byte("persistence_enabled: true\nstate_file: route-state.json\n"))
	if errParse == nil {
		t.Fatal("relative state_file was accepted")
	}
	if !strings.Contains(errParse.Error(), "absolute") {
		t.Fatalf("error = %q, want absolute-path validation", errParse)
	}
}

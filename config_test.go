package main

import (
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

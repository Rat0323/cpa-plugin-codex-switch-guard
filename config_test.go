package main

import "testing"

func TestConfigDefaultsRouteAffinityToEnabled(t *testing.T) {
	cfg, errParse := parsePluginConfig(nil)
	if errParse != nil {
		t.Fatalf("parsePluginConfig(nil) error = %v", errParse)
	}
	if !cfg.RouteAffinity {
		t.Fatalf("route_affinity default = false, want true")
	}
}

func TestConfigCanDisableRouteAffinity(t *testing.T) {
	cfg, errParse := parsePluginConfig([]byte("route_affinity: false\n"))
	if errParse != nil {
		t.Fatalf("parsePluginConfig() error = %v", errParse)
	}
	if cfg.RouteAffinity {
		t.Fatalf("route_affinity = true, want false")
	}
}

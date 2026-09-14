package main

import "testing"

func TestPersistenceDefaultsEnabledAndCanBeDisabled(t *testing.T) {
	defaults, errParse := parsePluginConfig(nil)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if !defaults.PersistenceEnabled {
		t.Fatal("persistence default = disabled, want enabled")
	}

	disabled, errParse := parsePluginConfig([]byte("persistence_enabled: false\n"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if disabled.PersistenceEnabled {
		t.Fatal("persistence_enabled: false was ignored")
	}
}

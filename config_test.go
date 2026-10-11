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

//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestABIRegistrationAdvertisesLifecycleAndSchema(t *testing.T) {
	t.Cleanup(func() {
		switchSafeABIState.Lock()
		switchSafeABIState.plugin = nil
		switchSafeABIState.Unlock()
	})

	for _, tc := range []struct {
		name          string
		schemaVersion uint32
		wantVersion   uint32
		wantError     bool
	}{
		{name: "CPA 7.2.130 schema 3", schemaVersion: 3, wantVersion: 3},
		{name: "CPA 7.2.145 schema 4", schemaVersion: 4, wantVersion: 4},
		{name: "CPA 7.2.151 schema 5", schemaVersion: 5, wantVersion: 5},
		{name: "current schema", schemaVersion: pluginSchemaVersion, wantVersion: pluginSchemaVersion},
		{name: "future schema", schemaVersion: pluginSchemaVersion + 1, wantVersion: pluginSchemaVersion},
		{name: "schema 0", schemaVersion: 0, wantError: true},
		{name: "schema 1", schemaVersion: 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, errMarshal := json.Marshal(abiLifecycleRequest{SchemaVersion: tc.schemaVersion})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			raw, errRegister := handleABIMethod(context.Background(), pluginabi.MethodPluginRegister, request)
			if tc.wantError {
				if errRegister == nil {
					t.Fatalf("register schema %d succeeded, want error", tc.schemaVersion)
				}
				return
			}
			if errRegister != nil {
				t.Fatalf("register schema %d: %v", tc.schemaVersion, errRegister)
			}

			var envelope abiEnvelope
			if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			var result abiRegistration
			if errUnmarshal := json.Unmarshal(envelope.Result, &result); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if result.SchemaVersion != tc.wantVersion {
				t.Fatalf("schema_version = %d, want %d", result.SchemaVersion, tc.wantVersion)
			}
			if !result.Capabilities.RequestInterceptor || !result.Capabilities.RequestLifecyclePlugin || !result.Capabilities.ManagementAPI {
				t.Fatalf("capabilities = %#v", result.Capabilities)
			}
		})
	}
}

func TestABIManagementStatus(t *testing.T) {
	t.Cleanup(func() {
		switchSafeABIState.Lock()
		switchSafeABIState.plugin = nil
		switchSafeABIState.Unlock()
	})

	registerRequest, errMarshal := json.Marshal(abiLifecycleRequest{SchemaVersion: pluginSchemaVersion})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if _, errRegister := handleABIMethod(context.Background(), pluginabi.MethodPluginRegister, registerRequest); errRegister != nil {
		t.Fatal(errRegister)
	}

	rawRegistration, errManagement := handleABIMethod(context.Background(), pluginabi.MethodManagementRegister, []byte(`{}`))
	if errManagement != nil {
		t.Fatal(errManagement)
	}
	var registrationEnvelope abiEnvelope
	if errDecode := json.Unmarshal(rawRegistration, &registrationEnvelope); errDecode != nil {
		t.Fatal(errDecode)
	}
	var registration struct {
		Routes []struct {
			Method string
			Path   string
		}
		Resources []json.RawMessage
	}
	if errDecode := json.Unmarshal(registrationEnvelope.Result, &registration); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(registration.Routes) != 1 || registration.Routes[0].Method != http.MethodGet || registration.Routes[0].Path != "/plugins/codex-switch-safe/status" || len(registration.Resources) != 0 {
		t.Fatalf("registration = %#v", registration)
	}

	handleRequest, errMarshal := json.Marshal(map[string]any{
		"Method": http.MethodGet,
		"Path":   "/v0/management/plugins/codex-switch-safe/status",
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	rawStatus, errHandle := handleABIMethod(context.Background(), pluginabi.MethodManagementHandle, handleRequest)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var statusEnvelope abiEnvelope
	if errDecode := json.Unmarshal(rawStatus, &statusEnvelope); errDecode != nil {
		t.Fatal(errDecode)
	}
	var response struct {
		StatusCode int
		Body       []byte
	}
	if errDecode := json.Unmarshal(statusEnvelope.Result, &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status response = %#v", response)
	}
	var status managementStatus
	if errDecode := json.Unmarshal(response.Body, &status); errDecode != nil {
		t.Fatal(errDecode)
	}
	if status.Plugin.ID != pluginID {
		t.Fatalf("status = %#v", status)
	}
}

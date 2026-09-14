package main

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Test-only conveniences bound to the process-local digester. Production code
// must pass the plugin's digester explicitly because it may be restored from
// the persisted state file and differ from the process key.

func inspectPayload(raw []byte) (payloadInspection, error) {
	return inspectPayloadWithDigester(raw, processOpaqueDigester)
}

func routeBoundInputItem(raw json.RawMessage) (string, string, bool) {
	return routeBoundInputItemWithDigester(raw, processOpaqueDigester)
}

func (p payloadInspection) stripRouteBoundState() ([]byte, bool, error) {
	updated, stats, errStrip := p.stripRouteBoundStateWithStats()
	return updated, stats.changed(), errStrip
}

func (p payloadInspection) stripRetiredRouteBoundItems(fingerprints []string) ([]byte, bool, error) {
	updated, stats, errStrip := p.stripRetiredRouteBoundItemsWithDigester(processOpaqueDigester, fingerprints)
	return updated, stats.changed(), errStrip
}

func sessionKeyFromRequest(req pluginapi.RequestInterceptRequest, inspection payloadInspection) string {
	return sessionKeyFromRequestWithDigester(req, inspection, processOpaqueDigester)
}

func opaqueDigest(namespace string, parts ...string) string {
	return processOpaqueDigester.digest(namespace, parts...)
}

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const maxOpaqueIdentifierLength = 256

func sessionKeyFromRequest(req pluginapi.RequestInterceptRequest, inspection payloadInspection) string {
	return sessionKeyFromRequestWithDigester(req, inspection, processOpaqueDigester)
}

func sessionKeyFromRequestWithDigester(req pluginapi.RequestInterceptRequest, inspection payloadInspection, digester opaqueDigester) string {
	callerScope := normalizeOpaqueIdentifier(metadataString(req.Metadata, "caller_scope"))
	if kind, value := requestSessionIdentity(req, inspection); value != "" {
		return digester.digest("session-v1", callerScope, kind, value)
	}
	return ""
}

func requestSessionIdentity(req pluginapi.RequestInterceptRequest, inspection payloadInspection) (string, string) {
	// CPA assigns execution_session_id to long-lived downstream executions.
	// It is more precise than a client header when nested agents share a root
	// caller, so prefer it whenever the host provides it.
	if value := normalizeOpaqueIdentifier(metadataString(req.Metadata, "execution_session_id")); value != "" {
		return "execution", value
	}
	if kind, value := headerSessionIdentity(req.Headers); value != "" {
		return kind, value
	}
	if kind, value := payloadSessionIdentity(inspection.Root); value != "" {
		return kind, value
	}
	if value := normalizedRawJSONString(inspection.Root["prompt_cache_key"]); value != "" {
		return "payload-prompt-cache-key", value
	}
	if value := normalizeOpaqueIdentifier(metadataString(req.Metadata, "derived_session_id")); value != "" {
		return "derived", value
	}
	return "", ""
}

func payloadSessionIdentity(root map[string]json.RawMessage) (string, string) {
	if len(root) == 0 {
		return "", ""
	}
	if value := codexConversationIdentity(
		normalizedRawJSONString(root["session_id"]),
		normalizedRawJSONString(root["thread_id"]),
	); value != "" {
		return "payload-codex-session", value
	}
	for _, field := range []string{"session_id", "sessionId", "conversation_id", "conversationId", "thread_id", "threadId"} {
		if value := normalizedRawJSONString(root[field]); value != "" {
			return "payload-" + strings.ToLower(field), value
		}
	}
	if metadata := normalizedRawJSONString(root["x-codex-turn-metadata"]); metadata != "" {
		if kind, value := codexTurnMetadataIdentity(metadata); value != "" {
			return kind, value
		}
	}
	if clientMetadataRaw, exists := root["client_metadata"]; exists {
		var clientMetadata map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(clientMetadataRaw, &clientMetadata); errUnmarshal == nil {
			if metadata := normalizedRawJSONString(clientMetadata["x-codex-turn-metadata"]); metadata != "" {
				if kind, value := codexTurnMetadataIdentity(metadata); value != "" {
					return kind, value
				}
			}
			if value := codexConversationIdentity(
				normalizedRawJSONString(clientMetadata["session_id"]),
				normalizedRawJSONString(clientMetadata["thread_id"]),
			); value != "" {
				return "client-metadata-codex-session", value
			}
			for _, field := range []string{"session_id", "sessionId", "conversation_id", "conversationId", "thread_id", "threadId"} {
				if value := normalizedRawJSONString(clientMetadata[field]); value != "" {
					return "client-metadata-" + strings.ToLower(field), value
				}
			}
		}
	}
	return "", ""
}

func headerSessionIdentity(headers http.Header) (string, string) {
	if value := codexConversationIdentity(
		normalizeOpaqueIdentifier(headers.Get("Session-Id")),
		normalizeOpaqueIdentifier(headers.Get("Thread-Id")),
	); value != "" {
		return "header-codex-session", value
	}
	for _, header := range []string{"X-Session-ID", "Session-Id", "Session_id", "X-Session-Affinity", "Thread-Id", "X-Codex-Thread-Id"} {
		if value := normalizeOpaqueIdentifier(headers.Get(header)); value != "" {
			return "header-" + strings.ToLower(header), value
		}
	}
	if metadata := normalizeOpaqueIdentifier(headers.Get("X-Codex-Turn-Metadata")); metadata != "" {
		return codexTurnMetadataIdentity(metadata)
	}
	return "", ""
}

func codexTurnMetadataIdentity(raw string) (string, string) {
	var metadata map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(raw), &metadata); errUnmarshal != nil {
		return "", ""
	}
	if value := codexConversationIdentity(
		normalizedRawJSONString(metadata["session_id"]),
		normalizedRawJSONString(metadata["thread_id"]),
	); value != "" {
		return "codex-turn-session", value
	}
	for _, field := range []string{"session_id", "conversation_id", "thread_id"} {
		if value := normalizedRawJSONString(metadata[field]); value != "" {
			return "codex-turn-" + strings.ToLower(field), value
		}
	}
	return "", ""
}

func codexConversationIdentity(sessionID, threadID string) string {
	sessionID = normalizeOpaqueIdentifier(sessionID)
	threadID = normalizeOpaqueIdentifier(threadID)
	if sessionID == "" && threadID == "" {
		return ""
	}
	return sessionID + "\x00" + threadID
}

func normalizedRawJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
		return ""
	}
	return normalizeOpaqueIdentifier(value)
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func normalizeOpaqueIdentifier(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxOpaqueIdentifierLength {
		return ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return raw
}

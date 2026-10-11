package main

// CPA exposes these keys from sdk/cliproxy/executor. Keep the small metadata
// boundary local instead of importing that executor package and its translator
// dependency graph into the native plugin.
const (
	selectedAuthMetadataKey       = "selected_auth_id"
	executionSessionMetadataKey   = "execution_session_id"
	derivedSessionIDMetadataKey   = "derived_session_id"
	canonicalSessionIDMetadataKey = "canonical_session_id"
	callerScopeMetadataKey        = "caller_scope"
)

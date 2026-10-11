package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPersistentRouteStateSurvivesRestartOnSameRoute(t *testing.T) {
	root := t.TempDir()
	cfg := persistentTestConfig(t, root)
	now := time.Date(2026, time.October, 11, 21, 0, 0, 0, time.UTC)
	store := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now })
	defer store.stopPersistence()

	req := codexRequest("seed", "session-a", "thread-a", "auth-a", "model-a", `{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	inspection, errInspect := store.inspectPayload(req.Body)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	sessionKey := store.sessionKey(req, inspection)
	route, routeKnown := store.routeFingerprint(req)
	if sessionKey == "" || !routeKnown {
		t.Fatalf("sessionKey=%q routeKnown=%v", sessionKey, routeKnown)
	}
	store.prepare("seed", sessionKey, route, true, stateSignals{})
	store.complete(pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded})
	store.stopPersistence()

	restarted := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now.Add(time.Minute) })
	defer restarted.stopPersistence()
	restartedInspection, errRestartInspect := restarted.inspectPayload(req.Body)
	if errRestartInspect != nil {
		t.Fatal(errRestartInspect)
	}
	restartedSession := restarted.sessionKey(req, restartedInspection)
	restartedRoute, restartedRouteKnown := restarted.routeFingerprint(req)
	if restartedSession != sessionKey || restartedRoute.Digest != route.Digest {
		t.Fatalf("identity changed across restart\nfirst=%q/%q\nsecond=%q/%q", sessionKey, route.Digest, restartedSession, restartedRoute.Digest)
	}

	decision := restarted.prepare("continuation", restartedSession, restartedRoute, restartedRouteKnown, stateSignals{HasReasoning: true, HasPreviousResponseID: true})
	if decision.Relation != routeRelationSame || decision.Strip || decision.Reject {
		t.Fatalf("same-route decision after restart = %#v", decision)
	}
}

func TestPersistentRouteStateDoesNotRestorePendingRequests(t *testing.T) {
	root := t.TempDir()
	cfg := persistentTestConfig(t, root)
	now := time.Date(2026, time.October, 11, 21, 0, 0, 0, time.UTC)
	store := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now })

	req := codexRequest("pending", "session-a", "thread-a", "auth-a", "model-a", `{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	inspection, errInspect := store.inspectPayload(req.Body)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	sessionKey := store.sessionKey(req, inspection)
	route, _ := store.routeFingerprint(req)
	store.prepare("pending", sessionKey, route, true, stateSignals{})
	store.stopPersistence()

	restarted := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now.Add(time.Second) })
	defer restarted.stopPersistence()
	if len(restarted.pending) != 0 {
		t.Fatalf("pending requests were restored: %#v", restarted.pending)
	}
	decision := restarted.prepare("after-restart", sessionKey, route, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationUnknown || !decision.Strip {
		t.Fatalf("pending continuation was trusted after restart: %#v", decision)
	}
}

func TestPersistentRouteStateCoalescesSavesWithoutShutdown(t *testing.T) {
	root := t.TempDir()
	cfg := persistentTestConfig(t, root)
	now := time.Date(2026, time.October, 11, 21, 0, 0, 0, time.UTC)
	store := newPersistentRouteStateStoreWithRootAndDebounce(cfg, root, func() time.Time { return now }, time.Millisecond)
	defer store.stopPersistence()

	route := persistentTestRoute()
	session := persistentTestSession()
	seedCommittedRoute(store, session, route)

	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, errRead := os.ReadFile(cfg.StateFile)
		if errRead == nil && strings.Contains(string(raw), `"has_route":true`) {
			break
		} else if errRead != nil && !os.IsNotExist(errRead) {
			t.Fatal(errRead)
		}
		if time.Now().After(deadline) {
			t.Fatal("coalesced persistence did not write the state file")
		}
		time.Sleep(5 * time.Millisecond)
	}

	restarted := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now.Add(time.Second) })
	defer restarted.stopPersistence()
	decision := restarted.prepare("after-async-save", session, route, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationSame || decision.Strip {
		t.Fatalf("async save did not restore committed route: %#v", decision)
	}
}

func TestPersistentRouteStateExpiresOldSessionsOnLoad(t *testing.T) {
	root := t.TempDir()
	cfg := persistentTestConfig(t, root)
	now := time.Date(2026, time.October, 11, 21, 0, 0, 0, time.UTC)
	store := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now })
	route := persistentTestRoute()
	session := persistentTestSession()
	seedCommittedRoute(store, session, route)
	store.stopPersistence()

	restarted := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now.Add(cfg.StateTTL + time.Nanosecond) })
	defer restarted.stopPersistence()
	decision := restarted.prepare("after-expiry", session, route, true, stateSignals{HasCompaction: true})
	if !decision.Reject || decision.Relation == routeRelationSame {
		t.Fatalf("expired persisted session was trusted: %#v", decision)
	}
}

func TestPersistentRouteStateFailsClosedWhenTampered(t *testing.T) {
	root := t.TempDir()
	cfg := persistentTestConfig(t, root)
	now := time.Date(2026, time.October, 11, 21, 0, 0, 0, time.UTC)
	store := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now })
	route := persistentTestRoute()
	session := persistentTestSession()
	seedCommittedRoute(store, session, route)
	store.stopPersistence()

	raw, errRead := os.ReadFile(cfg.StateFile)
	if errRead != nil {
		t.Fatal(errRead)
	}
	tampered := strings.Replace(string(raw), `"has_route":true`, `"has_route":false`, 1)
	if tampered == string(raw) {
		t.Fatalf("state seed did not contain a route flag: %s", raw)
	}
	if errWrite := os.WriteFile(cfg.StateFile, []byte(tampered), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	restarted := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now.Add(time.Second) })
	defer restarted.stopPersistence()
	decision := restarted.prepare("after-tamper", session, route, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationUnknown || !decision.Strip {
		t.Fatalf("tampered state was trusted: %#v", decision)
	}
	status := restarted.persistenceStatus()
	if !status.Enabled || status.Healthy {
		t.Fatalf("persistence status = %#v, want enabled and unhealthy", status)
	}
}

func TestPersistentStateFileAndKeyContainOnlyOpaqueData(t *testing.T) {
	root := t.TempDir()
	cfg := persistentTestConfig(t, root)
	now := time.Date(2026, time.October, 11, 21, 0, 0, 0, time.UTC)
	store := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now })

	req := codexRequest("seed", "session-private", "thread-private", "auth-private", "model-private", `{"input":[{"type":"message","role":"user","content":"conversation-private"}]}`)
	inspection, errInspect := store.inspectPayload(req.Body)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	sessionKey := store.sessionKey(req, inspection)
	route, _ := store.routeFingerprint(req)
	store.prepare("seed", sessionKey, route, true, stateSignals{})
	store.complete(pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded})
	store.stopPersistence()

	state, errState := os.ReadFile(cfg.StateFile)
	if errState != nil {
		t.Fatal(errState)
	}
	for _, value := range []string{"session-private", "thread-private", "auth-private", "model-private", "conversation-private"} {
		if strings.Contains(string(state), value) {
			t.Fatalf("state file leaked %q: %s", value, state)
		}
	}
	key, errKey := os.ReadFile(cfg.StateFile + ".key")
	if errKey != nil {
		t.Fatal(errKey)
	}
	if len(key) != sha256.Size {
		t.Fatalf("key file length = %d, want %d", len(key), sha256.Size)
	}
}

func TestPersistentRouteStateFailsClosedWhenKeyIsMissing(t *testing.T) {
	root := t.TempDir()
	cfg := persistentTestConfig(t, root)
	now := time.Date(2026, time.October, 11, 21, 0, 0, 0, time.UTC)
	store := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now })
	route := persistentTestRoute()
	session := persistentTestSession()
	seedCommittedRoute(store, session, route)
	store.stopPersistence()

	if errRemove := os.Remove(cfg.StateFile + ".key"); errRemove != nil {
		t.Fatal(errRemove)
	}
	restarted := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now.Add(time.Second) })
	defer restarted.stopPersistence()
	decision := restarted.prepare("after-key-loss", session, route, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationUnknown || !decision.Strip {
		t.Fatalf("state was trusted after key loss: %#v", decision)
	}
	status := restarted.persistenceStatus()
	if !status.Enabled || status.Healthy {
		t.Fatalf("persistence status = %#v, want enabled and unhealthy", status)
	}
}

func TestPersistentStatusErrorDoesNotLeakStateFilePath(t *testing.T) {
	root := t.TempDir()
	cfg := persistentTestConfig(t, root)
	if errMkdir := os.Mkdir(cfg.StateFile, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	now := time.Date(2026, time.October, 11, 21, 0, 0, 0, time.UTC)
	store := newPersistentRouteStateStoreWithRoot(cfg, root, func() time.Time { return now })
	seedCommittedRoute(store, persistentTestSession(), persistentTestRoute())
	store.stopPersistence()

	status := store.persistenceStatus()
	if !status.Enabled || status.Healthy || status.LastError == "" {
		t.Fatalf("persistence status = %#v, want enabled, unhealthy, and an error", status)
	}
	if strings.Contains(status.LastError, cfg.StateFile) || strings.Contains(status.LastError, root) {
		t.Fatalf("persistence error leaked a path: %q", status.LastError)
	}
}

func persistentTestConfig(t *testing.T, root string) pluginConfig {
	t.Helper()
	stateDir := filepath.Join(root, "state")
	if errMkdir := os.Mkdir(stateDir, 0o700); errMkdir != nil && !os.IsExist(errMkdir) {
		t.Fatal(errMkdir)
	}
	cfg := testConfig()
	cfg.PersistenceEnabled = true
	cfg.StateFile = filepath.Join(stateDir, "route-state-v1.json")
	return cfg
}

func persistentTestRoute() routeFingerprint {
	return routeFingerprint{Digest: strings.Repeat("a", sha256.Size*2)}
}

func persistentTestSession() string {
	return strings.Repeat("b", sha256.Size*2)
}

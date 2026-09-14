package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginUsesPersistedDigesterAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-state.json")
	config := []byte(fmt.Sprintf("persistence_enabled: true\nstate_file: '%s'\n", strings.ReplaceAll(path, "'", "''")))

	first, errBuild := buildPlugin(config)
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	seed := codexRequest("seed", "session-secret", "thread-secret", "auth-secret", "gpt-5.6", `{"input":[{"type":"message","role":"user","content":"start"}]}`)
	if response, errIntercept := first.InterceptRequestAfterAuth(nil, seed); errIntercept != nil || response.Terminate || len(response.Body) != 0 {
		t.Fatalf("seed response = %#v, err=%v", response, errIntercept)
	}
	if errComplete := first.HandleRequestComplete(nil, pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded}); errComplete != nil {
		t.Fatal(errComplete)
	}

	second, errBuild := buildPlugin(config)
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	same := codexRequest("same", "session-secret", "thread-secret", "auth-secret", "gpt-5.6", `{"previous_response_id":"response-secret","input":[{"type":"reasoning","encrypted_content":"cipher-secret"}]}`)
	response, errIntercept := second.InterceptRequestAfterAuth(nil, same)
	if errIntercept != nil || response.Terminate || len(response.Body) != 0 {
		t.Fatalf("restart response = %#v, err=%v", response, errIntercept)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, secret := range []string{"session-secret", "thread-secret", "auth-secret", "response-secret", "cipher-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("plugin state contains raw secret %q: %s", secret, raw)
		}
	}
}

func TestPluginSanitizesRetiredItemsWithPersistedDigesterAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-state.json")
	config := []byte(fmt.Sprintf("persistence_enabled: true\nstate_file: '%s'\n", strings.ReplaceAll(path, "'", "''")))
	persistedDigester := newOpaqueDigester([]byte("01234567890123456789012345678901"))
	now := time.Now().UTC()
	replay := codexRequest("replay", "session-secret", "thread-secret", "auth-b", "gpt-5.6", `{"previous_response_id":"resp-current","input":[{"type":"reasoning","id":"rs-retired","encrypted_content":"foreign"},{"type":"reasoning","id":"rs-current","encrypted_content":"current"},{"type":"message","role":"user","content":"continue"}]}`)
	inspection, errInspect := inspectPayloadWithDigester(replay.Body, persistedDigester)
	if errInspect != nil {
		t.Fatal(errInspect)
	}
	sessionKey := sessionKeyFromRequestWithDigester(replay, inspection, persistedDigester)
	route := routeFingerprint{Digest: persistedDigester.digest("route-v1", "auth-b", "gpt-5.6", "codex")}
	retired := inspection.Signals.ItemFingerprints[0]
	document := persistentStateDocument{
		Version:   persistentStateVersion,
		DigestKey: base64.RawStdEncoding.EncodeToString(persistedDigester.key),
		Sequence:  2,
		SavedAt:   now,
		Sessions: map[string]sessionRouteState{
			sessionKey: {
				Route:            route,
				HasRoute:         true,
				Sequence:         1,
				UpdatedAt:        now,
				RetiredItems:     map[string]uint64{retired: 2},
				RetiredItemRoute: map[string]routeFingerprint{retired: route},
			},
		},
	}
	if errSave := (&statePersistence{path: path}).save(document); errSave != nil {
		t.Fatal(errSave)
	}

	plugin, errBuild := buildPlugin(config)
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	response, errIntercept := plugin.InterceptRequestAfterAuth(nil, replay)
	if errIntercept != nil || response.Terminate {
		t.Fatalf("restarted replay response = %#v, err=%v", response, errIntercept)
	}
	body := string(response.Body)
	if strings.Contains(body, "rs-retired") || !strings.Contains(body, "rs-current") || !strings.Contains(body, `"previous_response_id":"resp-current"`) {
		t.Fatalf("retired/current route state handling = %s", body)
	}
}

func TestPersistentRouteStateSurvivesRestartWithoutRawIdentifiers(t *testing.T) {
	now := time.Date(2026, time.September, 14, 8, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "route-state.json")
	cfg := testConfig()
	cfg.PersistenceEnabled = true
	cfg.StateFile = path

	first := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now })
	session := first.digester.digest("session-v1", "caller-secret", "execution", "session-secret")
	route := routeFingerprint{Digest: first.digester.digest("route-v1", "auth-secret", "gpt-secret", "codex")}
	retired := first.digester.digest("route-bound-item-v1", "reasoning", "encrypted-content", "ciphertext-secret")
	first.prepare("request-1", session, route, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: []string{retired},
	})
	if errComplete := first.complete(pluginapi.RequestCompletion{RequestID: "request-1", Outcome: pluginapi.RequestCompletionSucceeded}); errComplete != nil {
		t.Fatal(errComplete)
	}

	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, secret := range []string{"caller-secret", "session-secret", "auth-secret", "gpt-secret", "ciphertext-secret", "request-1"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("persistent state contains raw secret %q: %s", secret, raw)
		}
	}

	second := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now.Add(time.Minute) })
	restartedSession := second.digester.digest("session-v1", "caller-secret", "execution", "session-secret")
	restartedRoute := routeFingerprint{Digest: second.digester.digest("route-v1", "auth-secret", "gpt-secret", "codex")}
	decision := second.prepare("request-2", restartedSession, restartedRoute, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationSame || decision.Strip || decision.Reject {
		t.Fatalf("restart decision = %#v", decision)
	}
}

func TestPersistentRouteStateDoesNotCommitFailedCandidate(t *testing.T) {
	now := time.Date(2026, time.September, 14, 8, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "route-state.json")
	cfg := testConfig()
	cfg.PersistenceEnabled = true
	cfg.StateFile = path

	first := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now })
	session := first.digester.digest("session-v1", "caller", "execution", "session")
	routeA := routeFingerprint{Digest: first.digester.digest("route-v1", "auth-a", "model", "codex")}
	routeB := routeFingerprint{Digest: first.digester.digest("route-v1", "auth-b", "model", "codex")}
	first.prepare("seed", session, routeA, true, stateSignals{})
	if errComplete := first.complete(pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded}); errComplete != nil {
		t.Fatal(errComplete)
	}
	first.prepare("failed", session, routeB, true, stateSignals{HasReasoning: true})
	if errComplete := first.complete(pluginapi.RequestCompletion{RequestID: "failed", Outcome: pluginapi.RequestCompletionFailed, Error: "upstream unavailable"}); errComplete != nil {
		t.Fatal(errComplete)
	}

	second := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now.Add(time.Minute) })
	restartedSession := second.digester.digest("session-v1", "caller", "execution", "session")
	restartedA := routeFingerprint{Digest: second.digester.digest("route-v1", "auth-a", "model", "codex")}
	decision := second.prepare("verify", restartedSession, restartedA, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationSame || decision.Strip || decision.Reject {
		t.Fatalf("failed candidate changed persisted route: %#v", decision)
	}
}

func TestPersistentRouteStateDropsExpiredAndCorruptState(t *testing.T) {
	now := time.Date(2026, time.September, 14, 8, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "route-state.json")
	cfg := testConfig()
	cfg.StateTTL = time.Minute
	cfg.PersistenceEnabled = true
	cfg.StateFile = path

	first := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now })
	session := first.digester.digest("session-v1", "caller", "execution", "session")
	route := routeFingerprint{Digest: first.digester.digest("route-v1", "auth", "model", "codex")}
	first.prepare("seed", session, route, true, stateSignals{})
	if errComplete := first.complete(pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded}); errComplete != nil {
		t.Fatal(errComplete)
	}

	expired := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now.Add(2 * time.Minute) })
	expiredSession := expired.digester.digest("session-v1", "caller", "execution", "session")
	expiredRoute := routeFingerprint{Digest: expired.digester.digest("route-v1", "auth", "model", "codex")}
	decision := expired.prepare("expired", expiredSession, expiredRoute, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationUnknown || !decision.Strip {
		t.Fatalf("expired decision = %#v", decision)
	}

	if errWrite := os.WriteFile(path, []byte(`{"version":1,"digest_key":"broken"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	recovered := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now.Add(3 * time.Minute) })
	if recovered.persistenceWarning == nil {
		t.Fatal("corrupt state did not produce a recovery warning")
	}
	unknownSession := recovered.digester.digest("session-v1", "caller", "execution", "session")
	unknownRoute := routeFingerprint{Digest: recovered.digester.digest("route-v1", "auth", "model", "codex")}
	unknown := recovered.prepare("unknown", unknownSession, unknownRoute, true, stateSignals{HasReasoning: true})
	if unknown.Relation != routeRelationUnknown || !unknown.Strip {
		t.Fatalf("corrupt-state decision = %#v", unknown)
	}
}

func TestPersistentRouteStateKeepsConcurrentTaintAcrossRestart(t *testing.T) {
	now := time.Date(2026, time.September, 14, 8, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "route-state.json")
	cfg := testConfig()
	cfg.PersistenceEnabled = true
	cfg.StateFile = path

	first := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now })
	session := first.digester.digest("session-v1", "caller", "execution", "session")
	routeA := routeFingerprint{Digest: first.digester.digest("route-v1", "auth-a", "model", "codex")}
	routeB := routeFingerprint{Digest: first.digester.digest("route-v1", "auth-b", "model", "codex")}
	first.prepare("seed", session, routeA, true, stateSignals{})
	if errComplete := first.complete(pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded}); errComplete != nil {
		t.Fatal(errComplete)
	}
	first.prepare("concurrent-a", session, routeA, true, stateSignals{HasReasoning: true})
	first.prepare("concurrent-b", session, routeB, true, stateSignals{HasReasoning: true})

	second := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return now.Add(time.Minute) })
	restartedSession := second.digester.digest("session-v1", "caller", "execution", "session")
	restartedB := routeFingerprint{Digest: second.digester.digest("route-v1", "auth-b", "model", "codex")}
	decision := second.prepare("verify", restartedSession, restartedB, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationChanged || !decision.Strip || decision.Reject {
		t.Fatalf("restarted tainted decision = %#v", decision)
	}
}

func TestDefaultStatePathUsesPluginID(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	path, errPath := persistentStatePathWithRoot("", root)
	if errPath != nil {
		t.Fatal(errPath)
	}
	if want := filepath.Join(root, pluginID, "route-state-v1.json"); path != want {
		t.Fatalf("default state path = %q, want %q", path, want)
	}
}

func TestRouteDigestIsScopedToPluginID(t *testing.T) {
	digester := newOpaqueDigester([]byte("01234567890123456789012345678901"))
	got := digester.digest("route-v1", "auth", "model", "codex")
	hash := hmac.New(sha256.New, []byte("01234567890123456789012345678901"))
	_, _ = hash.Write([]byte(pluginID))
	for _, part := range []string{"route-v1", "auth", "model", "codex"} {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(part))
	}
	want := hex.EncodeToString(hash.Sum(nil))
	if got != want {
		t.Fatalf("route digest = %q, want plugin-scoped value %q", got, want)
	}
}

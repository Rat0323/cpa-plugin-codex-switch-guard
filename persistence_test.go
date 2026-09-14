package main

import (
	"crypto/hmac"
	"crypto/sha256"
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

func TestDefaultStatePathMigratesFromRouteGuardDirectory(t *testing.T) {
	// Exercise the path contract with an isolated root; the constructor uses the
	// same helpers with the host's user config directory.
	root := filepath.Join(t.TempDir(), "config")
	canonical, errCanonical := persistentStatePathWithRoot("", root)
	if errCanonical != nil {
		t.Fatal(errCanonical)
	}
	if !strings.Contains(canonical, pluginID) {
		t.Fatalf("canonical state path = %q, want plugin id %q", canonical, pluginID)
	}
	legacy := legacyPersistentStatePathsWithRoot(root)
	if len(legacy) != 2 || filepath.Clean(legacy[0]) == filepath.Clean(canonical) || filepath.Clean(legacy[1]) == filepath.Clean(canonical) {
		t.Fatalf("legacy state paths = %#v, canonical = %q", legacy, canonical)
	}

	cfg := testConfig()
	cfg.StateFile = legacy[0]
	seed := newPersistentRouteStateStoreWithClock(cfg, func() time.Time { return time.Now().UTC() })
	session := seed.digester.digest("session-v1", "caller", "execution", "session")
	route := routeFingerprint{Digest: seed.digester.digest("route-v1", "auth", "model", "codex")}
	seed.prepare("seed", session, route, true, stateSignals{})
	if errComplete := seed.complete(pluginapi.RequestCompletion{RequestID: "seed", Outcome: pluginapi.RequestCompletionSucceeded}); errComplete != nil {
		t.Fatal(errComplete)
	}

	cfg.StateFile = ""
	migrated := newPersistentRouteStateStoreWithClockAndRoot(cfg, time.Now, root)
	if migrated.persistenceWarning != nil {
		t.Fatal(migrated.persistenceWarning)
	}
	if _, errStat := os.Stat(canonical); errStat != nil {
		t.Fatalf("canonical state was not migrated: %v", errStat)
	}
	decision := migrated.prepare("verify", session, route, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationSame || decision.Strip || decision.Reject {
		t.Fatalf("migrated route decision = %#v", decision)
	}
}

func TestRouteDigestNamespaceRemainsStableAcrossRename(t *testing.T) {
	digester := newOpaqueDigester([]byte("01234567890123456789012345678901"))
	got := digester.digest("route-v1", "auth", "model", "codex")
	hash := hmac.New(sha256.New, []byte("01234567890123456789012345678901"))
	for _, part := range []string{"codex-route-guard", "route-v1", "auth", "model", "codex"} {
		if part != "codex-route-guard" {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write([]byte(part))
	}
	want := hex.EncodeToString(hash.Sum(nil))
	if got != want {
		t.Fatalf("route digest = %q, want legacy-compatible value %q", got, want)
	}
}

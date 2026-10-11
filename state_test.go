package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRouteStatePreservesSameSuccessfulRouteAndStripsChangedRoute(t *testing.T) {
	store := newRouteStateStore(testConfig())
	session := "session-a"
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}

	first := store.prepare("request-1", session, routeA, true, stateSignals{})
	if first.Strip || first.Reject {
		t.Fatalf("first decision = %#v", first)
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "request-1", Outcome: pluginapi.RequestCompletionSucceeded})

	same := store.prepare("request-2", session, routeA, true, stateSignals{HasReasoning: true, HasPreviousResponseID: true})
	if same.Relation != routeRelationSame || same.Strip || same.Reject {
		t.Fatalf("same-route decision = %#v", same)
	}

	changed := store.prepare("request-3", session, routeB, true, stateSignals{HasReasoning: true, HasPreviousResponseID: true})
	if changed.Relation != routeRelationChanged || !changed.Strip || changed.Reject {
		t.Fatalf("changed-route decision = %#v", changed)
	}
}

func TestRouteStateDoesNotCommitFailedCandidate(t *testing.T) {
	store := newRouteStateStore(testConfig())
	session := "session-a"
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}

	store.prepare("initial", session, routeA, true, stateSignals{})
	store.complete(pluginapi.RequestCompletion{RequestID: "initial", Outcome: pluginapi.RequestCompletionSucceeded})

	candidate := store.prepare("failed-candidate", session, routeB, true, stateSignals{HasReasoning: true})
	if !candidate.Strip {
		t.Fatalf("candidate decision = %#v, want strip", candidate)
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "failed-candidate", Outcome: pluginapi.RequestCompletionFailed, Error: "upstream unavailable"})

	stillA := store.prepare("same-a", session, routeA, true, stateSignals{HasReasoning: true})
	if stillA.Relation != routeRelationSame || stillA.Strip {
		t.Fatalf("state after failed candidate = %#v", stillA)
	}
	newB := store.prepare("retry-b", session, routeB, true, stateSignals{HasReasoning: true})
	if newB.Relation != routeRelationChanged || !newB.Strip {
		t.Fatalf("B after failed candidate = %#v", newB)
	}
}

func TestRouteStateUsesLastSelectedAttemptForRetry(t *testing.T) {
	store := newRouteStateStore(testConfig())
	session := "session-a"
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}

	store.prepare("retrying-request", session, routeA, true, stateSignals{})
	store.prepare("retrying-request", session, routeB, true, stateSignals{})
	store.complete(pluginapi.RequestCompletion{RequestID: "retrying-request", Outcome: pluginapi.RequestCompletionSucceeded})

	decision := store.prepare("next", session, routeB, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationSame || decision.Strip || decision.Reject {
		t.Fatalf("retry commit decision = %#v", decision)
	}
}

func TestRouteStateDoesNotRetireItemsFromFailedCandidate(t *testing.T) {
	store := newRouteStateStore(testConfig())
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}
	seedCommittedRoute(store, "session-a", routeA)

	failedB := store.prepare("retry", "session-a", routeB, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: []string{"item-from-a"},
	})
	if !failedB.Strip {
		t.Fatalf("failed B decision = %#v", failedB)
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "retry", Outcome: pluginapi.RequestCompletionFailed, Error: "upstream unavailable"})

	retryA := store.prepare("retry-a", "session-a", routeA, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: []string{"item-from-a"},
	})
	if retryA.Relation != routeRelationSame || retryA.Strip || retryA.Reject || len(retryA.BlockedItemFingerprints) != 0 {
		t.Fatalf("retry A decision = %#v", retryA)
	}
}

func TestRouteStateConcurrentRouteChangesStayTaintedAndCannotRollBackNewerRoute(t *testing.T) {
	store := newRouteStateStore(testConfig())
	session := "session-a"
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}
	routeC := routeFingerprint{Digest: "route-c"}

	store.prepare("initial", session, routeA, true, stateSignals{})
	store.complete(pluginapi.RequestCompletion{RequestID: "initial", Outcome: pluginapi.RequestCompletionSucceeded})
	store.prepare("older", session, routeB, true, stateSignals{HasReasoning: true})
	store.prepare("newer", session, routeC, true, stateSignals{HasReasoning: true})

	store.complete(pluginapi.RequestCompletion{RequestID: "newer", Outcome: pluginapi.RequestCompletionSucceeded})
	store.complete(pluginapi.RequestCompletion{RequestID: "older", Outcome: pluginapi.RequestCompletionSucceeded})

	state := store.sessions["session-a"]
	if !state.Route.equal(routeC) || !state.Tainted {
		t.Fatalf("state after concurrent route changes = %#v", state)
	}
	decision := store.prepare("verify", session, routeC, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationChanged || !decision.Strip || decision.Reject {
		t.Fatalf("tainted route state = %#v", decision)
	}
}

func TestRouteStateSameRouteConcurrencyDoesNotTaint(t *testing.T) {
	store := newRouteStateStore(testConfig())
	route := routeFingerprint{Digest: "route-a"}
	seedCommittedRoute(store, "session-a", route)

	first := store.prepare("first", "session-a", route, true, stateSignals{HasReasoning: true})
	second := store.prepare("second", "session-a", route, true, stateSignals{HasReasoning: true})
	if first.Strip || second.Strip || first.Reject || second.Reject {
		t.Fatalf("same-route concurrent decisions = %#v, %#v", first, second)
	}
	if state := store.sessions["session-a"]; state.Tainted {
		t.Fatalf("same-route concurrency tainted state: %#v", state)
	}
}

func TestRouteStateTaintClearsOnlyAfterCleanSuccessfulTurn(t *testing.T) {
	store := newRouteStateStore(testConfig())
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}
	seedCommittedRoute(store, "session-a", routeA)

	store.prepare("older", "session-a", routeA, true, stateSignals{HasReasoning: true})
	store.prepare("newer", "session-a", routeB, true, stateSignals{HasReasoning: true})
	store.complete(pluginapi.RequestCompletion{RequestID: "newer", Outcome: pluginapi.RequestCompletionSucceeded})
	store.complete(pluginapi.RequestCompletion{RequestID: "older", Outcome: pluginapi.RequestCompletionSucceeded})
	if !store.sessions["session-a"].Tainted {
		t.Fatal("expected concurrent cross-route state to be tainted")
	}

	// A clean request can establish a new unambiguous route only after the
	// conflicting requests have finished.
	clean := store.prepare("clean", "session-a", routeB, true, stateSignals{})
	if clean.Strip || clean.Reject {
		t.Fatalf("clean reset decision = %#v", clean)
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "clean", Outcome: pluginapi.RequestCompletionSucceeded})
	if state := store.sessions["session-a"]; state.Tainted || !state.Route.equal(routeB) {
		t.Fatalf("clean reset did not establish route B: %#v", state)
	}

	resumed := store.prepare("resumed", "session-a", routeB, true, stateSignals{HasReasoning: true})
	if resumed.Relation != routeRelationSame || resumed.Strip || resumed.Reject {
		t.Fatalf("resumed reasoning decision = %#v", resumed)
	}
}

func TestRouteStateCompactionPolicies(t *testing.T) {
	blockCfg := testConfig()
	blockStore := newRouteStateStore(blockCfg)
	seedCommittedRoute(blockStore, "session-a", routeFingerprint{Digest: "route-a"})
	blocked := blockStore.prepare("block", "session-a", routeFingerprint{Digest: "route-b"}, true, stateSignals{HasCompaction: true})
	if !blocked.Reject || blocked.Strip {
		t.Fatalf("block decision = %#v", blocked)
	}
	if len(blockStore.pending) != 0 {
		t.Fatalf("blocked request became pending: %#v", blockStore.pending)
	}

	stripCfg := testConfig()
	stripCfg.CompactionPolicy = compactionPolicyStrip
	stripStore := newRouteStateStore(stripCfg)
	seedCommittedRoute(stripStore, "session-a", routeFingerprint{Digest: "route-a"})
	stripped := stripStore.prepare("strip", "session-a", routeFingerprint{Digest: "route-b"}, true, stateSignals{HasCompaction: true})
	if stripped.Reject || !stripped.Strip {
		t.Fatalf("strip decision = %#v", stripped)
	}
}

func TestRouteStateRetiresRejectedForeignCompaction(t *testing.T) {
	store := newRouteStateStore(testConfig())
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}
	seedCommittedRoute(store, "session-a", routeA)
	foreign := stateSignals{HasCompaction: true, ItemFingerprints: []string{"compaction-from-a"}}
	rejected := store.prepare("reject", "session-a", routeB, true, foreign)
	if !rejected.Reject || rejected.Strip {
		t.Fatalf("rejected compaction decision = %#v", rejected)
	}

	store.prepare("clean-b", "session-a", routeB, true, stateSignals{})
	store.complete(pluginapi.RequestCompletion{RequestID: "clean-b", Outcome: pluginapi.RequestCompletionSucceeded})
	replay := store.prepare("replay", "session-a", routeB, true, foreign)
	if replay.Relation != routeRelationSame || replay.Strip || replay.Reject || len(replay.BlockedItemFingerprints) != 1 {
		t.Fatalf("replayed compaction decision = %#v", replay)
	}
}

func TestRouteStateRetiresRejectedCompactionWithoutPriorRoute(t *testing.T) {
	store := newRouteStateStore(testConfig())
	route := routeFingerprint{Digest: "route-b"}
	foreign := stateSignals{HasCompaction: true, ItemFingerprints: []string{"unknown-compaction"}}
	rejected := store.prepare("reject", "session-a", route, true, foreign)
	if !rejected.Reject || rejected.Relation != routeRelationUnknown {
		t.Fatalf("rejected compaction decision = %#v", rejected)
	}
	store.prepare("clean", "session-a", route, true, stateSignals{})
	store.complete(pluginapi.RequestCompletion{RequestID: "clean", Outcome: pluginapi.RequestCompletionSucceeded})
	replay := store.prepare("replay", "session-a", route, true, foreign)
	if replay.Relation != routeRelationSame || len(replay.BlockedItemFingerprints) != 1 {
		t.Fatalf("replayed compaction decision = %#v", replay)
	}
}

func TestRouteStateRejectedCompactionDoesNotPoisonOriginalRoute(t *testing.T) {
	store := newRouteStateStore(testConfig())
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}
	seedCommittedRoute(store, "session-a", routeA)
	foreign := stateSignals{
		HasReasoning:     true,
		HasCompaction:    true,
		ItemFingerprints: []string{"item-from-a"},
	}

	rejected := store.prepare("reject-b", "session-a", routeB, true, foreign)
	if !rejected.Reject || rejected.Strip {
		t.Fatalf("rejected compaction decision = %#v", rejected)
	}

	replayA := store.prepare("replay-a", "session-a", routeA, true, foreign)
	if replayA.Relation != routeRelationSame || replayA.Strip || replayA.Reject || len(replayA.BlockedItemFingerprints) != 0 {
		t.Fatalf("original route was poisoned by rejected candidate = %#v", replayA)
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "replay-a", Outcome: pluginapi.RequestCompletionSucceeded})

	cleanB := store.prepare("clean-b", "session-a", routeB, true, stateSignals{})
	if cleanB.Reject || cleanB.Strip {
		t.Fatalf("clean route B decision = %#v", cleanB)
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "clean-b", Outcome: pluginapi.RequestCompletionSucceeded})

	replayB := store.prepare("replay-b", "session-a", routeB, true, foreign)
	if replayB.Relation != routeRelationSame || replayB.Strip || replayB.Reject || len(replayB.BlockedItemFingerprints) != 1 {
		t.Fatalf("rejected candidate was not protected on route B = %#v", replayB)
	}
}

func TestRouteStateUnknownRejectedCompactionDoesNotMutateSession(t *testing.T) {
	store := newRouteStateStore(testConfig())
	foreign := stateSignals{HasCompaction: true, ItemFingerprints: []string{"unknown-compaction"}}

	rejected := store.prepare("reject", "session-a", routeFingerprint{}, false, foreign)
	if !rejected.Reject || rejected.Strip {
		t.Fatalf("unknown rejected compaction decision = %#v", rejected)
	}
	if _, exists := store.sessions["session-a"]; exists {
		t.Fatalf("unknown rejected compaction mutated session: %#v", store.sessions["session-a"])
	}
}

func TestRouteStateExpiresAndBoundsEntries(t *testing.T) {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.StateTTL = time.Minute
	cfg.MaxSessions = 1
	cfg.MaxPending = 1
	store := newRouteStateStoreWithClock(cfg, func() time.Time { return now })
	seedCommittedRoute(store, "session-a", routeFingerprint{Digest: "route-a"})
	now = now.Add(2 * time.Minute)
	expired := store.prepare("after-ttl", "session-a", routeFingerprint{Digest: "route-a"}, true, stateSignals{HasReasoning: true})
	if expired.Relation != routeRelationUnknown || !expired.Strip {
		t.Fatalf("expired decision = %#v", expired)
	}

	store.prepare("pending-a", "session-a", routeFingerprint{Digest: "route-a"}, true, stateSignals{})
	now = now.Add(time.Second)
	store.prepare("pending-b", "session-b", routeFingerprint{Digest: "route-b"}, true, stateSignals{})
	if len(store.pending) != 1 {
		t.Fatalf("pending length = %d, want 1", len(store.pending))
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "pending-b", Outcome: pluginapi.RequestCompletionSucceeded})
	if len(store.sessions) != 1 {
		t.Fatalf("session length = %d, want 1", len(store.sessions))
	}
}

func TestInvalidEncryptedContentInvalidatesMatchingCommittedRoute(t *testing.T) {
	store := newRouteStateStore(testConfig())
	route := routeFingerprint{Digest: "route-a"}
	seedCommittedRoute(store, "session-a", route)
	store.prepare("bad", "session-a", route, true, stateSignals{HasReasoning: true})
	store.complete(pluginapi.RequestCompletion{
		RequestID: "bad",
		Outcome:   pluginapi.RequestCompletionFailed,
		Error:     "thinking_signature_invalid: invalid_encrypted_content",
	})
	decision := store.prepare("next", "session-a", route, true, stateSignals{HasReasoning: true})
	if decision.Relation != routeRelationUnknown || !decision.Strip {
		t.Fatalf("invalidated route decision = %#v", decision)
	}
}

func TestRouteStateBlocksRetiredItemsAfterRouteChangeSucceeds(t *testing.T) {
	store := newRouteStateStore(testConfig())
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}
	seedCommittedRoute(store, "session-a", routeA)

	changed := store.prepare("switch", "session-a", routeB, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: []string{"item-from-a"},
	})
	if !changed.Strip || len(changed.BlockedItemFingerprints) != 0 {
		t.Fatalf("changed route decision = %#v", changed)
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "switch", Outcome: pluginapi.RequestCompletionSucceeded})

	replay := store.prepare("same-b", "session-a", routeB, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: []string{"item-from-a", "item-from-b"},
	})
	if replay.Relation != routeRelationSame || replay.Strip || replay.Reject {
		t.Fatalf("same route replay decision = %#v", replay)
	}
	if len(replay.BlockedItemFingerprints) != 1 || replay.BlockedItemFingerprints[0] != "item-from-a" {
		t.Fatalf("blocked fingerprints = %#v", replay.BlockedItemFingerprints)
	}
}

func TestRouteStateRetiresItemsWithoutPriorCommittedSession(t *testing.T) {
	store := newRouteStateStore(testConfig())
	route := routeFingerprint{Digest: "route-b"}
	first := store.prepare("first", "session-a", route, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: []string{"foreign-item"},
	})
	if !first.Strip || first.Relation != routeRelationUnknown {
		t.Fatalf("first decision = %#v", first)
	}
	store.complete(pluginapi.RequestCompletion{RequestID: "first", Outcome: pluginapi.RequestCompletionSucceeded})
	replay := store.prepare("replay", "session-a", route, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: []string{"foreign-item", "current-item"},
	})
	if replay.Relation != routeRelationSame || replay.Strip || len(replay.BlockedItemFingerprints) != 1 || replay.BlockedItemFingerprints[0] != "foreign-item" {
		t.Fatalf("replay decision = %#v", replay)
	}
}

func TestRouteStateCompletionRestoresRetiredItemsAfterSessionEviction(t *testing.T) {
	store := newRouteStateStore(testConfig())
	routeA := routeFingerprint{Digest: "route-a"}
	routeB := routeFingerprint{Digest: "route-b"}
	seedCommittedRoute(store, "session-a", routeA)
	store.prepare("switch", "session-a", routeB, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: []string{"item-from-a"},
	})
	store.mu.Lock()
	delete(store.sessions, "session-a")
	store.mu.Unlock()
	store.complete(pluginapi.RequestCompletion{RequestID: "switch", Outcome: pluginapi.RequestCompletionSucceeded})

	state, exists := store.sessions["session-a"]
	if !exists || !state.HasRoute || !state.Route.equal(routeB) {
		t.Fatalf("restored session = %#v, exists=%v", state, exists)
	}
	if _, exists := state.RetiredItems["item-from-a"]; !exists {
		t.Fatalf("retired items were not restored: %#v", state.RetiredItems)
	}
}

func TestRouteStateStopsTrackingRetiredItemFingerprintsAfterOverflow(t *testing.T) {
	store := newRouteStateStore(testConfig())
	seedCommittedRoute(store, "session-a", routeFingerprint{Digest: "route-a"})
	fingerprints := make([]string, maxRetiredItemFingerprintsPerSession+1)
	for index := range fingerprints {
		fingerprints[index] = fmt.Sprintf("item-%03d", index)
	}
	store.prepare("switch", "session-a", routeFingerprint{Digest: "route-b"}, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: fingerprints,
	})
	store.complete(pluginapi.RequestCompletion{RequestID: "switch", Outcome: pluginapi.RequestCompletionSucceeded})
	state := store.sessions["session-a"]
	if !state.RetiredOverflow {
		t.Fatal("retired overflow marker = false, want true")
	}
	if len(state.RetiredItems) != 0 || len(state.RetiredItemRoute) != 0 {
		t.Fatalf("retired fingerprints retained after overflow: items=%d routes=%d", len(state.RetiredItems), len(state.RetiredItemRoute))
	}

	conservative := store.prepare("after-overflow", "session-a", routeFingerprint{Digest: "route-b"}, true, stateSignals{HasReasoning: true, ItemFingerprints: []string{"old-item"}})
	if !conservative.Strip || len(conservative.BlockedItemFingerprints) != 0 {
		t.Fatalf("overflow replay decision = %#v", conservative)
	}
}

func TestRouteStateBoundsPendingRetiredItemFingerprints(t *testing.T) {
	store := newRouteStateStore(testConfig())
	seedCommittedRoute(store, "session-a", routeFingerprint{Digest: "route-a"})
	fingerprints := make([]string, maxRetiredItemFingerprintsPerSession*4)
	for index := range fingerprints {
		fingerprints[index] = fmt.Sprintf("item-%04d", index)
	}

	store.prepare("switch", "session-a", routeFingerprint{Digest: "route-b"}, true, stateSignals{
		HasReasoning:     true,
		ItemFingerprints: fingerprints,
	})
	pending := store.pending[pendingKey("switch")]
	if len(pending.RetiredItemFingerprints) > maxRetiredItemFingerprintsPerSession {
		t.Fatalf("pending retired length = %d, want at most %d", len(pending.RetiredItemFingerprints), maxRetiredItemFingerprintsPerSession)
	}
}

func testConfig() pluginConfig {
	cfg := defaultPluginConfig()
	cfg.StateTTL = time.Hour
	cfg.MaxSessions = 16
	cfg.MaxPending = 16
	return cfg
}

func seedCommittedRoute(store *routeStateStore, session string, route routeFingerprint) {
	store.prepare("seed-"+session, session, route, true, stateSignals{})
	store.complete(pluginapi.RequestCompletion{RequestID: "seed-" + session, Outcome: pluginapi.RequestCompletionSucceeded})
}

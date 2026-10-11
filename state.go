package main

import (
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type routeFingerprint struct {
	Digest string
}

// opaqueDigestKey is process-local on purpose. The plugin never persists
// routing state, and a randomized key avoids making in-memory identifiers
// linkable across restarts or plugin reloads.
var opaqueDigestKey = newOpaqueDigestKey()

func newOpaqueDigestKey() []byte {
	key := make([]byte, sha256.Size)
	if _, errRead := cryptorand.Read(key); errRead == nil {
		return key
	}

	// crypto/rand is expected to be available on supported CPA hosts. Keep a
	// per-process fallback so an unexpected OS entropy failure does not turn
	// the digest into a stable cross-restart identifier.
	fallback := sha256.Sum256([]byte(pluginID + "\x00" + strconv.FormatInt(time.Now().UnixNano(), 10)))
	return fallback[:]
}

func routeFingerprintFromRequest(req pluginapi.RequestInterceptRequest) (routeFingerprint, bool) {
	authID := normalizeOpaqueIdentifier(metadataString(req.Metadata, selectedAuthMetadataKey))
	model := normalizeOpaqueIdentifier(req.Model)
	toFormat := strings.ToLower(strings.TrimSpace(req.ToFormat))
	if authID == "" || model == "" || toFormat != "codex" {
		return routeFingerprint{}, false
	}
	return routeFingerprint{Digest: opaqueDigest("route-v1", authID, model, toFormat)}, true
}

func (r routeFingerprint) equal(other routeFingerprint) bool {
	return r.Digest != "" && r.Digest == other.Digest
}

func opaqueDigest(namespace string, parts ...string) string {
	hash := hmac.New(sha256.New, opaqueDigestKey)
	_, _ = hash.Write([]byte(pluginID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(namespace))
	for _, part := range parts {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type routeRelation uint8

const (
	routeRelationUnknown routeRelation = iota
	routeRelationSame
	routeRelationChanged
)

type routeDecision struct {
	Relation                routeRelation
	Strip                   bool
	Reject                  bool
	BlockedItemFingerprints []string
}

const maxRetiredItemFingerprintsPerSession = 256

type sessionRouteState struct {
	Route            routeFingerprint
	HasRoute         bool
	Sequence         uint64
	UpdatedAt        time.Time
	Tainted          bool
	RetiredItems     map[string]uint64
	RetiredItemRoute map[string]routeFingerprint
	RetiredOverflow  bool
}

type pendingRoute struct {
	SessionKey              string
	Route                   routeFingerprint
	RouteKnown              bool
	Sequence                uint64
	ObservedAt              time.Time
	CleanStart              bool
	CanClearTaint           bool
	RetiredOverflow         bool
	RetiredItemFingerprints []string
}

type routeStateStore struct {
	mu               sync.Mutex
	cfg              pluginConfig
	now              func() time.Time
	nextPurge        time.Time
	sequence         uint64
	sessions         map[string]sessionRouteState
	pending          map[string]pendingRoute
	pendingBySession map[string]*pendingSessionIndex
}

type pendingSessionIndex struct {
	Count       int
	RouteCounts map[string]int
}

type routeStateSnapshot struct {
	Sessions         int `json:"sessions"`
	Pending          int `json:"pending"`
	TaintedSessions  int `json:"tainted_sessions"`
	OverflowSessions int `json:"overflow_sessions"`
}

func newRouteStateStore(cfg pluginConfig) *routeStateStore {
	return newRouteStateStoreWithClock(cfg, time.Now)
}

func newRouteStateStoreWithClock(cfg pluginConfig, now func() time.Time) *routeStateStore {
	if now == nil {
		now = time.Now
	}
	return &routeStateStore{
		cfg:              cfg,
		now:              now,
		sessions:         make(map[string]sessionRouteState),
		pending:          make(map[string]pendingRoute),
		pendingBySession: make(map[string]*pendingSessionIndex),
	}
}

func (s *routeStateStore) snapshot() routeStateSnapshot {
	if s == nil {
		return routeStateSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybePurgeExpiredLocked(s.now())
	snapshot := routeStateSnapshot{
		Sessions: len(s.sessions),
		Pending:  len(s.pending),
	}
	for _, session := range s.sessions {
		if session.Tainted {
			snapshot.TaintedSessions++
		}
		if session.RetiredOverflow {
			snapshot.OverflowSessions++
		}
	}
	return snapshot
}

// prepare records only the final selected-auth candidate for a logical request.
// A route becomes reusable only after its corresponding lifecycle success.
func (s *routeStateStore) prepare(requestID, sessionKey string, route routeFingerprint, routeKnown bool, signals stateSignals) routeDecision {
	if s == nil {
		return failClosedDecision(signals, compactionPolicyBlock)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.maybePurgeExpiredLocked(now)

	requestID = strings.TrimSpace(requestID)
	if requestID != "" {
		// CPA calls the after-auth hook for each retry candidate. Replace the
		// previous candidate for this one logical request instead of treating it
		// as independent concurrency.
		s.deletePendingLocked(pendingKey(requestID))
	}

	decision := routeDecision{Relation: routeRelationUnknown}
	canTrack := sessionKey != "" && routeKnown
	if !canTrack {
		decision = failClosedDecision(signals, s.cfg.CompactionPolicy)
		if decision.Strip && sessionKey != "" && requestID != "" {
			retiredItemFingerprints, retiredOverflow := boundedUniqueFingerprints(signals.ItemFingerprints)
			session := s.sessions[sessionKey]
			session.UpdatedAt = now
			s.sessions[sessionKey] = session
			s.trimSessionsLocked()
			s.sequence++
			s.setPendingLocked(pendingKey(requestID), pendingRoute{
				SessionKey:              sessionKey,
				RouteKnown:              false,
				Sequence:                s.sequence,
				ObservedAt:              now,
				CleanStart:              true,
				RetiredOverflow:         retiredOverflow,
				RetiredItemFingerprints: retiredItemFingerprints,
			})
			s.trimPendingLocked()
		}
		return decision
	}

	session, hasSession := s.sessions[sessionKey]
	hasOtherPending := s.hasPendingForSessionLocked(sessionKey)
	if s.hasConcurrentRouteMismatchLocked(sessionKey, route) {
		// Different credentials/models in flight for one conversation create an
		// ambiguous continuation order. Do not guess which encrypted state wins.
		session.Tainted = true
		session.UpdatedAt = now
		hasSession = true
	}

	if session.HasRoute && !session.Tainted {
		if session.Route.equal(route) {
			decision.Relation = routeRelationSame
			if session.RetiredOverflow && (signals.HasReasoning || signals.HasCompaction) {
				decision.Strip = true
			} else {
				decision.BlockedItemFingerprints = retiredItemMatches(session.RetiredItems, session.RetiredItemRoute, signals.ItemFingerprints, route)
			}
		} else {
			decision.Relation = routeRelationChanged
		}
	} else if session.HasRoute {
		decision.Relation = routeRelationChanged
	}

	unsafeState := signals.hasRouteBoundState() && (session.Tainted || decision.Relation != routeRelationSame)
	if unsafeState && signals.HasCompaction && s.cfg.CompactionPolicy == compactionPolicyBlock {
		decision.Reject = true
		// A locally rejected candidate never commits a route transition. Keep a
		// route-scoped barrier for a known target so a replay on that target is
		// still removed, without poisoning the previously committed route.
		if sessionKey != "" && routeKnown {
			session = s.retireItemsLocked(session, signals.ItemFingerprints, route, true, now)
			s.sessions[sessionKey] = session
			s.trimSessionsLocked()
		}
		return decision
	}
	if unsafeState {
		decision.Strip = true
	}

	if requestID == "" {
		return decision
	}
	if !hasSession {
		session = sessionRouteState{}
	}
	session.UpdatedAt = now
	s.sessions[sessionKey] = session
	s.trimSessionsLocked()

	cleanStart := decision.Strip || !signals.hasRouteBoundState()
	var retiredItemFingerprints []string
	var retiredOverflow bool
	if decision.Strip {
		retiredItemFingerprints, retiredOverflow = boundedUniqueFingerprints(signals.ItemFingerprints)
	}
	s.sequence++
	s.setPendingLocked(pendingKey(requestID), pendingRoute{
		SessionKey:              sessionKey,
		Route:                   route,
		RouteKnown:              true,
		Sequence:                s.sequence,
		ObservedAt:              now,
		CleanStart:              cleanStart,
		CanClearTaint:           session.Tainted && cleanStart && !hasOtherPending,
		RetiredOverflow:         retiredOverflow,
		RetiredItemFingerprints: retiredItemFingerprints,
	})
	s.trimPendingLocked()
	return decision
}

func failClosedDecision(signals stateSignals, policy compactionPolicy) routeDecision {
	decision := routeDecision{Relation: routeRelationUnknown}
	if !signals.hasRouteBoundState() {
		return decision
	}
	if signals.HasCompaction && policy == compactionPolicyBlock {
		decision.Reject = true
		return decision
	}
	decision.Strip = true
	return decision
}

func (s *routeStateStore) retireItemsLocked(session sessionRouteState, fingerprints []string, route routeFingerprint, routeKnown bool, now time.Time) sessionRouteState {
	if session.RetiredOverflow || len(fingerprints) == 0 {
		session.UpdatedAt = now
		return session
	}
	if len(fingerprints) > maxRetiredItemFingerprintsPerSession {
		return markRetiredOverflow(session, now)
	}
	if session.RetiredItems == nil {
		session.RetiredItems = make(map[string]uint64)
	}
	if session.RetiredItemRoute == nil {
		session.RetiredItemRoute = make(map[string]routeFingerprint)
	}
	for _, fingerprint := range fingerprints {
		if fingerprint == "" {
			continue
		}
		s.sequence++
		session.RetiredItems[fingerprint] = s.sequence
		if routeKnown {
			session.RetiredItemRoute[fingerprint] = route
		} else {
			delete(session.RetiredItemRoute, fingerprint)
		}
		if len(session.RetiredItems) > maxRetiredItemFingerprintsPerSession {
			return markRetiredOverflow(session, now)
		}
	}
	session.UpdatedAt = now
	return session
}

func boundedUniqueFingerprints(fingerprints []string) ([]string, bool) {
	if len(fingerprints) == 0 {
		return nil, false
	}
	if len(fingerprints) > maxRetiredItemFingerprintsPerSession {
		return nil, true
	}
	unique := make([]string, 0, min(len(fingerprints), maxRetiredItemFingerprintsPerSession))
	seen := make(map[string]struct{}, min(len(fingerprints), maxRetiredItemFingerprintsPerSession))
	for _, fingerprint := range fingerprints {
		if fingerprint == "" {
			continue
		}
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		if len(unique) == maxRetiredItemFingerprintsPerSession {
			return nil, true
		}
		seen[fingerprint] = struct{}{}
		unique = append(unique, fingerprint)
	}
	return unique, false
}

func markRetiredOverflow(session sessionRouteState, now time.Time) sessionRouteState {
	session.RetiredOverflow = true
	session.RetiredItems = nil
	session.RetiredItemRoute = nil
	session.UpdatedAt = now
	return session
}

func retiredItemMatches(retired map[string]uint64, retiredRoutes map[string]routeFingerprint, observed []string, route routeFingerprint) []string {
	if len(retired) == 0 || len(observed) == 0 {
		return nil
	}
	matches := make([]string, 0, len(observed))
	seen := make(map[string]struct{}, len(observed))
	for _, fingerprint := range observed {
		if _, exists := retired[fingerprint]; !exists {
			continue
		}
		if retiredRoute, routeScoped := retiredRoutes[fingerprint]; routeScoped && !retiredRoute.equal(route) {
			continue
		}
		if _, duplicate := seen[fingerprint]; duplicate {
			continue
		}
		seen[fingerprint] = struct{}{}
		matches = append(matches, fingerprint)
	}
	return matches
}

func (s *routeStateStore) complete(completion pluginapi.RequestCompletion) bool {
	if s == nil {
		return false
	}
	requestID := strings.TrimSpace(completion.RequestID)
	if requestID == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.maybePurgeExpiredLocked(now)
	pending, exists := s.pending[pendingKey(requestID)]
	if !exists {
		return false
	}
	s.deletePendingLocked(pendingKey(requestID))

	current, existsCurrent := s.sessions[pending.SessionKey]
	if !existsCurrent {
		current = sessionRouteState{}
	}

	if completion.Outcome == pluginapi.RequestCompletionSucceeded {
		if pending.RetiredOverflow {
			current = markRetiredOverflow(current, now)
		} else if len(pending.RetiredItemFingerprints) > 0 {
			current = s.retireItemsLocked(current, pending.RetiredItemFingerprints, pending.Route, pending.RouteKnown, now)
		}
		applied := false
		if !pending.RouteKnown {
			applied = true
		} else if !current.HasRoute || pending.Sequence >= current.Sequence {
			current.Route = pending.Route
			current.HasRoute = true
			current.Sequence = pending.Sequence
			current.UpdatedAt = now
			applied = true
		}
		// A tainted session can only become reusable after an unambiguous clean
		// request completed while no other request in that session was active.
		if pending.RouteKnown && pending.CanClearTaint && pending.CleanStart && applied && !s.hasPendingForSessionLocked(pending.SessionKey) {
			current.Tainted = false
		}
		s.sessions[pending.SessionKey] = current
		s.trimSessionsLocked()
		return true
	}

	if !hasInvalidEncryptedContentError(completion.Error) {
		return true
	}

	// The upstream has rejected a route-bound item despite the selected route.
	// Clear the binding and force a clean restart for the next continuation.
	current.Tainted = true
	current.UpdatedAt = now
	if current.HasRoute && current.Route.equal(pending.Route) && current.Sequence <= pending.Sequence {
		current.Route = routeFingerprint{}
		current.HasRoute = false
		current.Sequence = pending.Sequence
	}
	s.sessions[pending.SessionKey] = current
	s.trimSessionsLocked()
	return true
}

func (s *routeStateStore) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence = 0
	s.nextPurge = time.Time{}
	s.sessions = make(map[string]sessionRouteState)
	s.pending = make(map[string]pendingRoute)
	s.pendingBySession = make(map[string]*pendingSessionIndex)
}

func (s *routeStateStore) discardPending(requestID string) {
	if s == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletePendingLocked(pendingKey(requestID))
}

func pendingKey(requestID string) string {
	return opaqueDigest("pending-v1", strings.TrimSpace(requestID))
}

func (s *routeStateStore) setPendingLocked(key string, pending pendingRoute) {
	if existing, exists := s.pending[key]; exists {
		s.removePendingIndexLocked(existing)
	}
	s.pending[key] = pending
	if pending.SessionKey == "" {
		return
	}
	index := s.pendingBySession[pending.SessionKey]
	if index == nil {
		index = &pendingSessionIndex{RouteCounts: make(map[string]int)}
		s.pendingBySession[pending.SessionKey] = index
	}
	index.Count++
	index.RouteCounts[pendingRouteIndexKey(pending)]++
}

func (s *routeStateStore) deletePendingLocked(key string) (pendingRoute, bool) {
	pending, exists := s.pending[key]
	if !exists {
		return pendingRoute{}, false
	}
	delete(s.pending, key)
	s.removePendingIndexLocked(pending)
	return pending, true
}

func (s *routeStateStore) removePendingIndexLocked(pending pendingRoute) {
	index := s.pendingBySession[pending.SessionKey]
	if index == nil {
		return
	}
	index.Count--
	routeKey := pendingRouteIndexKey(pending)
	index.RouteCounts[routeKey]--
	if index.RouteCounts[routeKey] <= 0 {
		delete(index.RouteCounts, routeKey)
	}
	if index.Count <= 0 {
		delete(s.pendingBySession, pending.SessionKey)
	}
}

func pendingRouteIndexKey(pending pendingRoute) string {
	if !pending.RouteKnown {
		return ""
	}
	return pending.Route.Digest
}

func (s *routeStateStore) maybePurgeExpiredLocked(now time.Time) {
	if !s.nextPurge.IsZero() && now.Before(s.nextPurge) {
		return
	}
	s.purgeExpiredLocked(now)
	interval := s.cfg.StateTTL / 16
	if interval < time.Second {
		interval = time.Second
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	s.nextPurge = now.Add(interval)
}

func (s *routeStateStore) purgeExpiredLocked(now time.Time) {
	for key, state := range s.sessions {
		if now.Sub(state.UpdatedAt) > s.cfg.StateTTL {
			delete(s.sessions, key)
		}
	}
	for requestID, state := range s.pending {
		if now.Sub(state.ObservedAt) > s.cfg.StateTTL {
			s.deletePendingLocked(requestID)
			s.taintSessionLocked(state.SessionKey, now)
		}
	}
}

func (s *routeStateStore) trimSessionsLocked() {
	if len(s.sessions) <= s.cfg.MaxSessions {
		return
	}
	activeSessions := make(map[string]struct{}, len(s.pendingBySession))
	for sessionKey := range s.pendingBySession {
		activeSessions[sessionKey] = struct{}{}
	}
	for len(s.sessions) > s.cfg.MaxSessions {
		var oldestKey string
		var oldest sessionRouteState
		for key, state := range s.sessions {
			if _, active := activeSessions[key]; active {
				continue
			}
			if oldestKey == "" || state.UpdatedAt.Before(oldest.UpdatedAt) || (state.UpdatedAt.Equal(oldest.UpdatedAt) && key < oldestKey) {
				oldestKey = key
				oldest = state
			}
		}
		if oldestKey == "" {
			for key, state := range s.sessions {
				if oldestKey == "" || state.UpdatedAt.Before(oldest.UpdatedAt) || (state.UpdatedAt.Equal(oldest.UpdatedAt) && key < oldestKey) {
					oldestKey = key
					oldest = state
				}
			}
		}
		delete(s.sessions, oldestKey)
	}
}

func (s *routeStateStore) trimPendingLocked() {
	for len(s.pending) > s.cfg.MaxPending {
		var oldestID string
		var oldest pendingRoute
		for requestID, state := range s.pending {
			if oldestID == "" || state.ObservedAt.Before(oldest.ObservedAt) || (state.ObservedAt.Equal(oldest.ObservedAt) && requestID < oldestID) {
				oldestID = requestID
				oldest = state
			}
		}
		s.deletePendingLocked(oldestID)
		s.taintSessionLocked(oldest.SessionKey, s.now())
	}
}

func (s *routeStateStore) taintSessionLocked(sessionKey string, now time.Time) {
	if sessionKey == "" {
		return
	}
	state := s.sessions[sessionKey]
	state.Tainted = true
	state.UpdatedAt = now
	s.sessions[sessionKey] = state
	s.trimSessionsLocked()
}

func (s *routeStateStore) hasConcurrentRouteMismatchLocked(sessionKey string, route routeFingerprint) bool {
	index := s.pendingBySession[sessionKey]
	return index != nil && index.Count > index.RouteCounts[route.Digest]
}

func (s *routeStateStore) hasPendingForSessionLocked(sessionKey string) bool {
	index := s.pendingBySession[sessionKey]
	return index != nil && index.Count > 0
}

func hasInvalidEncryptedContentError(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "invalid_encrypted_content") ||
		strings.Contains(message, "thinking_signature_invalid") ||
		strings.Contains(message, "encrypted content could not be decrypted")
}

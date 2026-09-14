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
	Digest string `json:"digest"`
}

type opaqueDigester struct {
	key []byte
}

var processOpaqueDigester = newOpaqueDigester(newOpaqueDigestKey())

func newOpaqueDigester(key []byte) opaqueDigester {
	cloned := append([]byte(nil), key...)
	if len(cloned) != sha256.Size {
		cloned = newOpaqueDigestKey()
	}
	return opaqueDigester{key: cloned}
}

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

func routeFingerprintFromRequestWithDigester(req pluginapi.RequestInterceptRequest, digester opaqueDigester) (routeFingerprint, bool) {
	authID := normalizeOpaqueIdentifier(metadataString(req.Metadata, "selected_auth_id"))
	model := normalizeOpaqueIdentifier(req.Model)
	toFormat := strings.ToLower(strings.TrimSpace(req.ToFormat))
	if authID == "" || model == "" || toFormat != "codex" {
		return routeFingerprint{}, false
	}
	return routeFingerprint{Digest: digester.digest("route-v1", authID, model, toFormat)}, true
}

func (r routeFingerprint) equal(other routeFingerprint) bool {
	return r.Digest != "" && r.Digest == other.Digest
}

func (d opaqueDigester) digest(namespace string, parts ...string) string {
	hash := hmac.New(sha256.New, d.key)
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
	Route            routeFingerprint            `json:"route"`
	HasRoute         bool                        `json:"has_route"`
	Sequence         uint64                      `json:"sequence"`
	UpdatedAt        time.Time                   `json:"updated_at"`
	Tainted          bool                        `json:"tainted"`
	RetiredItems     map[string]uint64           `json:"retired_items,omitempty"`
	RetiredItemRoute map[string]routeFingerprint `json:"retired_item_routes,omitempty"`
	RetiredOverflow  bool                        `json:"retired_overflow,omitempty"`
}

type pendingRoute struct {
	SessionKey              string
	Route                   routeFingerprint
	RouteKnown              bool
	Sequence                uint64
	ObservedAt              time.Time
	CleanStart              bool
	CanClearTaint           bool
	RetiredItemFingerprints []string
}

type routeStateStore struct {
	mu                   sync.Mutex
	cfg                  pluginConfig
	now                  func() time.Time
	sequence             uint64
	sessions             map[string]sessionRouteState
	pending              map[string]pendingRoute
	digester             opaqueDigester
	persistence          *statePersistence
	persistenceWarning   error
	lastPersistenceError error
}

func newRouteStateStore(cfg pluginConfig) *routeStateStore {
	return newRouteStateStoreWithClock(cfg, time.Now)
}

func newRouteStateStoreWithClock(cfg pluginConfig, now func() time.Time) *routeStateStore {
	if now == nil {
		now = time.Now
	}
	return &routeStateStore{
		cfg:      cfg,
		now:      now,
		sessions: make(map[string]sessionRouteState),
		pending:  make(map[string]pendingRoute),
		digester: processOpaqueDigester,
	}
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
	persistentDirty := s.purgeExpiredLocked(now)
	defer func() {
		if persistentDirty {
			s.persistSoonLocked()
		}
	}()

	requestID = strings.TrimSpace(requestID)
	if requestID != "" {
		// CPA calls the after-auth hook for each retry candidate. Replace the
		// previous candidate for this one logical request instead of treating it
		// as independent concurrency.
		delete(s.pending, s.pendingKey(requestID))
	}

	decision := routeDecision{Relation: routeRelationUnknown}
	canTrack := sessionKey != "" && routeKnown
	if !canTrack {
		decision = failClosedDecision(signals, s.cfg.CompactionPolicy)
		if decision.Strip && sessionKey != "" && requestID != "" {
			session := s.sessions[sessionKey]
			session.UpdatedAt = now
			s.sessions[sessionKey] = session
			s.trimSessionsLocked()
			s.sequence++
			s.pending[s.pendingKey(requestID)] = pendingRoute{
				SessionKey:              sessionKey,
				RouteKnown:              false,
				Sequence:                s.sequence,
				ObservedAt:              now,
				CleanStart:              true,
				RetiredItemFingerprints: uniqueFingerprints(signals.ItemFingerprints),
			}
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
		persistentDirty = true
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
			persistentDirty = true
			persistentDirty = s.trimSessionsLocked() || persistentDirty
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
	persistentDirty = s.trimSessionsLocked() || persistentDirty

	cleanStart := decision.Strip || !signals.hasRouteBoundState()
	var retiredItemFingerprints []string
	if decision.Strip {
		retiredItemFingerprints = uniqueFingerprints(signals.ItemFingerprints)
	}
	s.sequence++
	s.pending[s.pendingKey(requestID)] = pendingRoute{
		SessionKey:              sessionKey,
		Route:                   route,
		RouteKnown:              true,
		Sequence:                s.sequence,
		ObservedAt:              now,
		CleanStart:              cleanStart,
		CanClearTaint:           session.Tainted && cleanStart && !hasOtherPending,
		RetiredItemFingerprints: retiredItemFingerprints,
	}
	persistentDirty = s.trimPendingLocked() || persistentDirty
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
	if len(fingerprints) == 0 {
		session.UpdatedAt = now
		return session
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
	}
	for len(session.RetiredItems) > maxRetiredItemFingerprintsPerSession {
		session.RetiredOverflow = true
		var oldestFingerprint string
		var oldestSequence uint64
		for fingerprint, sequence := range session.RetiredItems {
			if oldestFingerprint == "" || sequence < oldestSequence || (sequence == oldestSequence && fingerprint < oldestFingerprint) {
				oldestFingerprint = fingerprint
				oldestSequence = sequence
			}
		}
		delete(session.RetiredItems, oldestFingerprint)
		delete(session.RetiredItemRoute, oldestFingerprint)
	}
	session.UpdatedAt = now
	return session
}

func uniqueFingerprints(fingerprints []string) []string {
	if len(fingerprints) == 0 {
		return nil
	}
	unique := make([]string, 0, len(fingerprints))
	seen := make(map[string]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint == "" {
			continue
		}
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		seen[fingerprint] = struct{}{}
		unique = append(unique, fingerprint)
	}
	return unique
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

func (s *routeStateStore) complete(completion pluginapi.RequestCompletion) error {
	if s == nil {
		return nil
	}
	requestID := strings.TrimSpace(completion.RequestID)
	if requestID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	persistentDirty := s.purgeExpiredLocked(now)
	pending, exists := s.pending[s.pendingKey(requestID)]
	if !exists {
		if persistentDirty {
			return s.persistLocked()
		}
		return nil
	}
	delete(s.pending, s.pendingKey(requestID))

	current, existsCurrent := s.sessions[pending.SessionKey]
	if !existsCurrent {
		current = sessionRouteState{}
	}

	if completion.Outcome == pluginapi.RequestCompletionSucceeded {
		if len(pending.RetiredItemFingerprints) > 0 {
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
		return s.persistLocked()
	}

	if !hasInvalidEncryptedContentError(completion.Error) {
		if persistentDirty {
			return s.persistLocked()
		}
		return nil
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
	return s.persistLocked()
}

func (s *routeStateStore) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence = 0
	s.sessions = make(map[string]sessionRouteState)
	s.pending = make(map[string]pendingRoute)
	s.persistSoonLocked()
}

func (s *routeStateStore) discardPending(requestID string) {
	if s == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, s.pendingKey(requestID))
}

func (s *routeStateStore) pendingKey(requestID string) string {
	return s.digester.digest("pending-v1", strings.TrimSpace(requestID))
}

func (s *routeStateStore) persistSoonLocked() {
	if errPersist := s.persistLocked(); errPersist != nil {
		s.lastPersistenceError = errPersist
	}
}

func (s *routeStateStore) takePersistenceError() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	errPersist := s.lastPersistenceError
	s.lastPersistenceError = nil
	return errPersist
}

func (s *routeStateStore) purgeExpiredLocked(now time.Time) bool {
	persistentDirty := false
	for key, state := range s.sessions {
		if now.Sub(state.UpdatedAt) > s.cfg.StateTTL {
			delete(s.sessions, key)
			persistentDirty = true
		}
	}
	for requestID, state := range s.pending {
		if now.Sub(state.ObservedAt) > s.cfg.StateTTL {
			delete(s.pending, requestID)
			s.taintSessionLocked(state.SessionKey, now)
			persistentDirty = true
		}
	}
	return persistentDirty
}

func (s *routeStateStore) trimSessionsLocked() bool {
	trimmed := false
	activeSessions := make(map[string]struct{})
	for _, pending := range s.pending {
		activeSessions[pending.SessionKey] = struct{}{}
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
		trimmed = true
	}
	return trimmed
}

func (s *routeStateStore) trimPendingLocked() bool {
	trimmed := false
	for len(s.pending) > s.cfg.MaxPending {
		var oldestID string
		var oldest pendingRoute
		for requestID, state := range s.pending {
			if oldestID == "" || state.ObservedAt.Before(oldest.ObservedAt) || (state.ObservedAt.Equal(oldest.ObservedAt) && requestID < oldestID) {
				oldestID = requestID
				oldest = state
			}
		}
		delete(s.pending, oldestID)
		s.taintSessionLocked(oldest.SessionKey, s.now())
		trimmed = true
	}
	return trimmed
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
	for _, pending := range s.pending {
		if pending.SessionKey == sessionKey && !pending.Route.equal(route) {
			return true
		}
	}
	return false
}

func (s *routeStateStore) hasPendingForSessionLocked(sessionKey string) bool {
	for _, pending := range s.pending {
		if pending.SessionKey == sessionKey {
			return true
		}
	}
	return false
}

func hasInvalidEncryptedContentError(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "invalid_encrypted_content") ||
		strings.Contains(message, "thinking_signature_invalid") ||
		strings.Contains(message, "encrypted content could not be decrypted")
}

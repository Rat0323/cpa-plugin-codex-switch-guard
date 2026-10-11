package main

import (
	"bufio"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	persistentStateVersion = 1
	maxPersistentStateSize = 128 << 20
	persistenceKeySize     = sha256.Size
)

type statePersistence struct {
	statePath    string
	keyPath      string
	integrityKey []byte
}

type persistentStateBody struct {
	Version  int                          `json:"version"`
	SavedAt  time.Time                    `json:"saved_at"`
	Sequence uint64                       `json:"sequence"`
	Sessions map[string]sessionRouteState `json:"sessions"`
}

type persistentStateDocument struct {
	Body      persistentStateBody `json:"body"`
	Integrity string              `json:"integrity"`
}

func newConfiguredRouteStateStore(cfg pluginConfig) *routeStateStore {
	if !cfg.PersistenceEnabled {
		return newRouteStateStore(cfg)
	}
	root, errRoot := os.UserConfigDir()
	if errRoot != nil {
		store := newRouteStateStore(cfg)
		store.persistenceWarning = fmt.Errorf("resolve user config directory: %w", errRoot)
		return store
	}
	return newPersistentRouteStateStoreWithRoot(cfg, root, time.Now)
}

func newPersistentRouteStateStoreWithRoot(cfg pluginConfig, root string, now func() time.Time) *routeStateStore {
	return newPersistentRouteStateStoreWithRootAndDebounce(cfg, root, now, time.Second)
}

func newPersistentRouteStateStoreWithRootAndDebounce(cfg pluginConfig, root string, now func() time.Time, debounce time.Duration) *routeStateStore {
	store := newRouteStateStoreWithClock(cfg, now)
	if !cfg.PersistenceEnabled {
		return store
	}
	if debounce <= 0 {
		debounce = time.Second
	}

	statePath := cfg.StateFile
	if strings.TrimSpace(statePath) == "" {
		if strings.TrimSpace(root) == "" {
			store.persistenceWarning = errors.New("user config directory is empty")
			return store
		}
		statePath = filepath.Join(root, pluginID, "route-state-v1.json")
	}

	persistence := &statePersistence{
		statePath: filepath.Clean(statePath),
		keyPath:   filepath.Clean(statePath + ".key"),
	}
	store.persistence = persistence

	masterKey, errKey := loadOrCreatePersistenceKey(persistence.keyPath)
	if errKey != nil {
		store.persistenceWarning = errKey
		return store
	}
	persistence.integrityKey = derivePersistenceKey(masterKey, "integrity-v1")
	store.digester = newOpaqueDigester(derivePersistenceKey(masterKey, "fingerprint-v1"))

	document, errLoad := persistence.load(store.cfg, store.now())
	if errLoad == nil {
		store.sequence = document.Body.Sequence
		store.sessions = document.Body.Sessions
		store.lastSavedAt = document.Body.SavedAt
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		store.persistenceWarning = errLoad
	}
	store.startPersistenceWriter(debounce)
	return store
}

func loadOrCreatePersistenceKey(path string) ([]byte, error) {
	info, errStat := os.Lstat(path)
	switch {
	case errStat == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("state key file must be a regular file")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("state key file permissions must not allow group or other access")
		}
		if info.Size() != persistenceKeySize {
			return nil, fmt.Errorf("state key file must contain %d bytes", persistenceKeySize)
		}
		file, errOpen := os.Open(path)
		if errOpen != nil {
			return nil, errOpen
		}
		defer file.Close()
		key, errRead := io.ReadAll(io.LimitReader(file, persistenceKeySize+1))
		if errRead != nil {
			return nil, errRead
		}
		if len(key) != persistenceKeySize {
			return nil, fmt.Errorf("state key file contains %d bytes, want %d", len(key), persistenceKeySize)
		}
		return key, nil
	case errors.Is(errStat, os.ErrNotExist):
		key := make([]byte, persistenceKeySize)
		if _, errRead := cryptorand.Read(key); errRead != nil {
			return nil, fmt.Errorf("generate state key: %w", errRead)
		}
		if errWrite := atomicWriteFile(path, key, 0o600); errWrite != nil {
			return nil, fmt.Errorf("create state key file: %w", errWrite)
		}
		return key, nil
	default:
		return nil, errStat
	}
}

func (p *statePersistence) load(cfg pluginConfig, now time.Time) (persistentStateDocument, error) {
	var document persistentStateDocument
	info, errStat := os.Lstat(p.statePath)
	if errStat != nil {
		return document, errStat
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return document, errors.New("state file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return document, errors.New("state file permissions must not allow group or other access")
	}
	if info.Size() > maxPersistentStateSize {
		return document, fmt.Errorf("state file exceeds %d bytes", maxPersistentStateSize)
	}

	file, errOpen := os.Open(p.statePath)
	if errOpen != nil {
		return document, errOpen
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxPersistentStateSize+1))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&document); errDecode != nil {
		return document, fmt.Errorf("decode state file: %w", errDecode)
	}
	if errExtra := ensureJSONEOF(decoder); errExtra != nil {
		return document, errExtra
	}
	if errValidate := validatePersistentState(document, cfg, now); errValidate != nil {
		return document, errValidate
	}
	if errIntegrity := p.verifyIntegrity(document.Body, document.Integrity); errIntegrity != nil {
		return document, errIntegrity
	}
	if document.Body.Sessions == nil {
		document.Body.Sessions = make(map[string]sessionRouteState)
	}
	for key, session := range document.Body.Sessions {
		if now.Sub(session.UpdatedAt) > cfg.StateTTL {
			delete(document.Body.Sessions, key)
		}
	}
	return document, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	errDecode := decoder.Decode(&extra)
	if errors.Is(errDecode, io.EOF) {
		return nil
	}
	if errDecode == nil {
		return errors.New("state file contains multiple JSON values")
	}
	return fmt.Errorf("decode trailing state data: %w", errDecode)
}

func validatePersistentState(document persistentStateDocument, cfg pluginConfig, now time.Time) error {
	if document.Body.Version != persistentStateVersion {
		return fmt.Errorf("unsupported state version %d", document.Body.Version)
	}
	if document.Body.SavedAt.IsZero() || document.Body.SavedAt.After(now.Add(5*time.Minute)) {
		return errors.New("invalid state saved_at timestamp")
	}
	if len(document.Body.Sessions) > cfg.MaxSessions {
		return fmt.Errorf("state has %d sessions; limit is %d", len(document.Body.Sessions), cfg.MaxSessions)
	}
	for sessionKey, session := range document.Body.Sessions {
		if !validOpaqueDigest(sessionKey) {
			return errors.New("invalid session fingerprint")
		}
		if session.UpdatedAt.IsZero() || session.UpdatedAt.After(now.Add(5*time.Minute)) {
			return errors.New("invalid session updated_at timestamp")
		}
		if session.Sequence > document.Body.Sequence {
			return errors.New("session sequence exceeds state sequence")
		}
		if session.HasRoute {
			if !validOpaqueDigest(session.Route.Digest) {
				return errors.New("invalid route fingerprint")
			}
		} else if session.Route.Digest != "" {
			return errors.New("route fingerprint present without a committed route")
		}
		if len(session.RetiredItems) > maxRetiredItemFingerprintsPerSession || len(session.RetiredItemRoute) > maxRetiredItemFingerprintsPerSession {
			return errors.New("retired item limit exceeded")
		}
		for fingerprint, sequence := range session.RetiredItems {
			if !validOpaqueDigest(fingerprint) {
				return errors.New("invalid retired item fingerprint")
			}
			if sequence > document.Body.Sequence {
				return errors.New("retired item sequence exceeds state sequence")
			}
		}
		for fingerprint, route := range session.RetiredItemRoute {
			if _, exists := session.RetiredItems[fingerprint]; !exists {
				return errors.New("retired item route has no matching item")
			}
			if !validOpaqueDigest(route.Digest) {
				return errors.New("invalid retired item route fingerprint")
			}
		}
	}
	return nil
}

func validOpaqueDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, errDecode := hex.DecodeString(value)
	return errDecode == nil && len(decoded) == sha256.Size
}

func (p *statePersistence) verifyIntegrity(body persistentStateBody, integrity string) error {
	expected, errDecode := hex.DecodeString(integrity)
	if errDecode != nil || len(expected) != sha256.Size {
		return errors.New("invalid state integrity tag")
	}
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return fmt.Errorf("encode state body for verification: %w", errMarshal)
	}
	mac := hmac.New(sha256.New, p.integrityKey)
	_, _ = mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), expected) {
		return errors.New("state integrity check failed")
	}
	return nil
}

func (s *routeStateStore) stopPersistence() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.persistence == nil || s.persistenceStopped {
		s.mu.Unlock()
		return
	}
	s.persistenceStopped = true
	stop := s.persistStop
	done := s.persistDone
	s.mu.Unlock()
	if stop == nil || done == nil {
		return
	}
	close(stop)
	<-done
}

func (s *routeStateStore) startPersistenceWriter(debounce time.Duration) {
	if s == nil || s.persistence == nil {
		return
	}
	s.mu.Lock()
	if s.persistStop != nil {
		s.mu.Unlock()
		return
	}
	s.persistDebounce = debounce
	s.persistWake = make(chan struct{}, 1)
	s.persistStop = make(chan struct{})
	s.persistDone = make(chan struct{})
	wake := s.persistWake
	stop := s.persistStop
	done := s.persistDone
	s.mu.Unlock()

	go func() {
		for {
			select {
			case <-wake:
				if waitForPersistenceDebounce(debounce, wake, stop) {
					s.persistNow()
					close(done)
					return
				}
				s.persistNow()
			case <-stop:
				s.persistNow()
				close(done)
				return
			}
		}
	}()
}

func waitForPersistenceDebounce(debounce time.Duration, wake, stop <-chan struct{}) bool {
	timer := time.NewTimer(debounce)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return false
		case <-stop:
			return true
		case <-wake:
			timer.Stop()
			timer.Reset(debounce)
		}
	}
}

func (s *routeStateStore) markPersistenceDirtyLocked() {
	if s == nil || s.persistence == nil || s.persistenceStopped || s.persistWake == nil {
		return
	}
	select {
	case s.persistWake <- struct{}{}:
	default:
	}
}

func (s *routeStateStore) persistNow() {
	if s == nil || s.persistence == nil {
		return
	}

	s.mu.Lock()
	body := persistentStateBody{
		Version:  persistentStateVersion,
		SavedAt:  s.now().UTC(),
		Sequence: s.sequence,
		Sessions: make(map[string]sessionRouteState, len(s.sessions)),
	}
	for key, session := range s.sessions {
		body.Sessions[key] = session
	}
	persistence := s.persistence
	s.mu.Unlock()

	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		s.recordPersistenceError(fmt.Errorf("encode state file: %w", errMarshal))
		return
	}
	mac := hmac.New(sha256.New, persistence.integrityKey)
	_, _ = mac.Write(raw)
	document := persistentStateDocument{
		Body:      body,
		Integrity: hex.EncodeToString(mac.Sum(nil)),
	}
	encoded, errEncode := json.Marshal(document)
	if errEncode != nil {
		s.recordPersistenceError(fmt.Errorf("encode state file: %w", errEncode))
		return
	}
	if len(encoded) > maxPersistentStateSize {
		s.recordPersistenceError(fmt.Errorf("state file exceeds %d bytes", maxPersistentStateSize))
		return
	}
	if errSave := atomicWriteFile(persistence.statePath, encoded, 0o600); errSave != nil {
		s.recordPersistenceError(fmt.Errorf("replace state file: %w", errSave))
		return
	}

	s.mu.Lock()
	s.lastPersistenceError = nil
	s.lastSavedAt = body.SavedAt
	s.mu.Unlock()
}

func (s *routeStateStore) recordPersistenceError(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastPersistenceError = err
	s.mu.Unlock()
}

func (s *routeStateStore) persistenceStatus() persistenceStatusSnapshot {
	if s == nil {
		return persistenceStatusSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := persistenceStatusSnapshot{
		Enabled:     s.cfg.PersistenceEnabled,
		Healthy:     s.persistenceWarning == nil && s.lastPersistenceError == nil,
		LastSavedAt: s.lastSavedAt,
	}
	if s.persistenceWarning != nil && s.lastPersistenceError != nil {
		status.LastError = safePersistenceErrorMessage(errors.Join(s.persistenceWarning, s.lastPersistenceError))
	} else if s.persistenceWarning != nil {
		status.LastError = safePersistenceErrorMessage(s.persistenceWarning)
	} else if s.lastPersistenceError != nil {
		status.LastError = safePersistenceErrorMessage(s.lastPersistenceError)
	}
	return status
}

func safePersistenceErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "state key or state file not found"
	case errors.Is(err, os.ErrPermission):
		return "state file permission denied"
	case strings.Contains(err.Error(), "state integrity check failed"):
		return "state integrity check failed"
	case strings.Contains(err.Error(), "unsupported state version"):
		return "unsupported state version"
	case strings.Contains(err.Error(), "state file exceeds"):
		return "state file too large"
	case strings.Contains(err.Error(), "must be a regular file"):
		return "state file must be a regular file"
	case strings.Contains(err.Error(), "permissions must not allow"):
		return "state file permissions are too broad"
	case strings.Contains(err.Error(), "invalid state"):
		return "invalid persistent state"
	default:
		return "persistence unavailable"
	}
}

func derivePersistenceKey(master []byte, domain string) []byte {
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte(pluginID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(domain))
	return mac.Sum(nil)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if errMkdir := os.MkdirAll(directory, 0o700); errMkdir != nil {
		return fmt.Errorf("create state directory: %w", errMkdir)
	}
	info, errStat := os.Lstat(directory)
	if errStat != nil {
		return fmt.Errorf("inspect state directory: %w", errStat)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("state directory must be a real directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return errors.New("state directory permissions must not allow group or other access")
	}

	temporary, errCreate := os.CreateTemp(directory, ".route-state-*")
	if errCreate != nil {
		return fmt.Errorf("create temporary state file: %w", errCreate)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if errChmod := temporary.Chmod(mode); errChmod != nil {
		cleanup()
		return fmt.Errorf("secure temporary state file: %w", errChmod)
	}
	writer := bufio.NewWriter(temporary)
	if _, errWrite := writer.Write(data); errWrite != nil {
		cleanup()
		return fmt.Errorf("write temporary state file: %w", errWrite)
	}
	if errFlush := writer.Flush(); errFlush != nil {
		cleanup()
		return fmt.Errorf("flush temporary state file: %w", errFlush)
	}
	if errSync := temporary.Sync(); errSync != nil {
		cleanup()
		return fmt.Errorf("sync temporary state file: %w", errSync)
	}
	if errClose := temporary.Close(); errClose != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close temporary state file: %w", errClose)
	}
	if errRename := os.Rename(temporaryPath, path); errRename != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace state file: %w", errRename)
	}
	if runtime.GOOS != "windows" {
		directoryFile, errOpen := os.Open(directory)
		if errOpen != nil {
			return fmt.Errorf("open state directory for sync: %w", errOpen)
		}
		if errSync := directoryFile.Sync(); errSync != nil {
			_ = directoryFile.Close()
			return fmt.Errorf("sync state directory: %w", errSync)
		}
		if errClose := directoryFile.Close(); errClose != nil {
			return fmt.Errorf("close state directory after sync: %w", errClose)
		}
	}
	return nil
}

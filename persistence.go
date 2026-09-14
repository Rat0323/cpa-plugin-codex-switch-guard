package main

import (
	"bufio"
	"encoding/base64"
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
)

type statePersistence struct {
	path string
}

type persistentStateDocument struct {
	Version   int                          `json:"version"`
	DigestKey string                       `json:"digest_key"`
	Sequence  uint64                       `json:"sequence"`
	SavedAt   time.Time                    `json:"saved_at"`
	Sessions  map[string]sessionRouteState `json:"sessions"`
}

func newPersistentRouteStateStore(cfg pluginConfig) *routeStateStore {
	return newPersistentRouteStateStoreWithClock(cfg, time.Now)
}

func newPersistentRouteStateStoreWithClock(cfg pluginConfig, now func() time.Time) *routeStateStore {
	root, errConfig := os.UserConfigDir()
	if errConfig != nil {
		store := newRouteStateStoreWithClock(cfg, now)
		store.persistenceWarning = fmt.Errorf("resolve user config directory: %w", errConfig)
		return store
	}
	return newPersistentRouteStateStoreWithClockAndRoot(cfg, now, root)
}

func newPersistentRouteStateStoreWithClockAndRoot(cfg pluginConfig, now func() time.Time, root string) *routeStateStore {
	store := newRouteStateStoreWithClock(cfg, now)
	path, errPath := persistentStatePathWithRoot(cfg.StateFile, root)
	if errPath != nil {
		store.persistenceWarning = errPath
		return store
	}
	store.persistence = &statePersistence{path: path}

	document, errLoad := store.persistence.load(cfg, store.now())
	loadedLegacy := false
	if errors.Is(errLoad, os.ErrNotExist) && strings.TrimSpace(cfg.StateFile) == "" {
		for _, legacyPath := range legacyPersistentStatePathsWithRoot(root) {
			legacy := &statePersistence{path: legacyPath}
			document, errLoad = legacy.load(cfg, store.now())
			if errLoad == nil {
				loadedLegacy = true
				break
			}
			if !errors.Is(errLoad, os.ErrNotExist) {
				break
			}
		}
	}
	if errLoad == nil {
		key, _ := base64.RawStdEncoding.DecodeString(document.DigestKey)
		store.digester = newOpaqueDigester(key)
		store.sequence = document.Sequence
		store.sessions = document.Sessions
		if loadedLegacy {
			if errPersist := store.persistLocked(); errPersist != nil {
				store.persistenceWarning = errPersist
			}
		}
		return store
	}
	if !errors.Is(errLoad, os.ErrNotExist) {
		store.persistenceWarning = errLoad
	}
	if errPersist := store.persistLocked(); errPersist != nil {
		store.persistenceWarning = errors.Join(store.persistenceWarning, errPersist)
	}
	return store
}

func persistentStatePath(configured string) (string, error) {
	root, errConfig := os.UserConfigDir()
	if errConfig != nil {
		return "", fmt.Errorf("resolve user config directory: %w", errConfig)
	}
	return persistentStatePathWithRoot(configured, root)
}

func persistentStatePathWithRoot(configured, root string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return filepath.Clean(configured), nil
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("user config directory is empty")
	}
	return filepath.Join(root, pluginID, "route-state-v1.json"), nil
}

func legacyPersistentStatePaths() []string {
	root, errConfig := os.UserConfigDir()
	if errConfig != nil {
		return nil
	}
	return legacyPersistentStatePathsWithRoot(root)
}

func legacyPersistentStatePathsWithRoot(root string) []string {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	return []string{
		filepath.Join(root, "codex-route-guard", "route-state-v1.json"),
		filepath.Join(root, "codex-switch-safe", "route-state-v1.json"),
	}
}

func (p *statePersistence) load(cfg pluginConfig, now time.Time) (persistentStateDocument, error) {
	var document persistentStateDocument
	info, errStat := os.Lstat(p.path)
	if errStat != nil {
		return document, errStat
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return document, fmt.Errorf("state file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return document, fmt.Errorf("state file permissions must not allow group or other access")
	}
	if info.Size() > maxPersistentStateSize {
		return document, fmt.Errorf("state file exceeds %d bytes", maxPersistentStateSize)
	}
	file, errOpen := os.Open(p.path)
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
	if errValidate := validatePersistentState(document, cfg); errValidate != nil {
		return document, errValidate
	}
	if document.Sessions == nil {
		document.Sessions = make(map[string]sessionRouteState)
	}
	for key, session := range document.Sessions {
		if session.UpdatedAt.IsZero() || now.Sub(session.UpdatedAt) > cfg.StateTTL || session.UpdatedAt.After(now.Add(5*time.Minute)) {
			delete(document.Sessions, key)
			continue
		}
		if session.Sequence > document.Sequence {
			document.Sequence = session.Sequence
		}
		for _, sequence := range session.RetiredItems {
			if sequence > document.Sequence {
				document.Sequence = sequence
			}
		}
	}
	return document, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if errDecode := decoder.Decode(&extra); !errors.Is(errDecode, io.EOF) {
		if errDecode == nil {
			return fmt.Errorf("state file contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing state data: %w", errDecode)
	}
	return nil
}

func validatePersistentState(document persistentStateDocument, cfg pluginConfig) error {
	if document.Version != persistentStateVersion {
		return fmt.Errorf("unsupported state version %d", document.Version)
	}
	key, errKey := base64.RawStdEncoding.DecodeString(document.DigestKey)
	if errKey != nil || len(key) != 32 {
		return fmt.Errorf("invalid state digest key")
	}
	if len(document.Sessions) > cfg.MaxSessions {
		return fmt.Errorf("state has %d sessions; limit is %d", len(document.Sessions), cfg.MaxSessions)
	}
	for sessionKey, session := range document.Sessions {
		if !validDigest(sessionKey) {
			return fmt.Errorf("invalid session fingerprint")
		}
		if session.HasRoute && !validDigest(session.Route.Digest) {
			return fmt.Errorf("invalid route fingerprint")
		}
		if len(session.RetiredItems) > maxRetiredItemFingerprintsPerSession || len(session.RetiredItemRoute) > maxRetiredItemFingerprintsPerSession {
			return fmt.Errorf("retired item limit exceeded")
		}
		for fingerprint := range session.RetiredItems {
			if !validDigest(fingerprint) {
				return fmt.Errorf("invalid retired item fingerprint")
			}
		}
		for fingerprint, route := range session.RetiredItemRoute {
			if _, exists := session.RetiredItems[fingerprint]; !exists || !validDigest(route.Digest) {
				return fmt.Errorf("invalid retired item route")
			}
		}
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, errDecode := hex.DecodeString(value)
	return errDecode == nil && len(decoded) == 32
}

func (s *routeStateStore) persistLocked() error {
	if s == nil || s.persistence == nil {
		return nil
	}
	document := persistentStateDocument{
		Version:   persistentStateVersion,
		DigestKey: base64.RawStdEncoding.EncodeToString(s.digester.key),
		Sequence:  s.sequence,
		SavedAt:   s.now().UTC(),
		Sessions:  s.sessions,
	}
	return s.persistence.save(document)
}

func (p *statePersistence) save(document persistentStateDocument) error {
	directory := filepath.Dir(p.path)
	if errMkdir := os.MkdirAll(directory, 0o700); errMkdir != nil {
		return fmt.Errorf("create state directory: %w", errMkdir)
	}
	if runtime.GOOS != "windows" {
		if info, errStat := os.Stat(directory); errStat != nil {
			return fmt.Errorf("inspect state directory: %w", errStat)
		} else if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("state directory permissions must not allow group or other access")
		}
	}

	temporary, errCreate := os.CreateTemp(directory, ".route-state-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create temporary state file: %w", errCreate)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if errChmod := temporary.Chmod(0o600); errChmod != nil {
		cleanup()
		return fmt.Errorf("secure temporary state file: %w", errChmod)
	}
	writer := bufio.NewWriter(temporary)
	encoder := json.NewEncoder(writer)
	if errEncode := encoder.Encode(document); errEncode != nil {
		cleanup()
		return fmt.Errorf("encode state file: %w", errEncode)
	}
	if errFlush := writer.Flush(); errFlush != nil {
		cleanup()
		return fmt.Errorf("flush state file: %w", errFlush)
	}
	if offset, errSeek := temporary.Seek(0, io.SeekCurrent); errSeek != nil {
		cleanup()
		return fmt.Errorf("measure state file: %w", errSeek)
	} else if offset > maxPersistentStateSize {
		cleanup()
		return fmt.Errorf("state file exceeds %d bytes", maxPersistentStateSize)
	}
	if errSync := temporary.Sync(); errSync != nil {
		cleanup()
		return fmt.Errorf("sync state file: %w", errSync)
	}
	if errClose := temporary.Close(); errClose != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close state file: %w", errClose)
	}
	if errRename := os.Rename(temporaryPath, p.path); errRename != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace state file: %w", errRename)
	}
	return nil
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type stateSignals struct {
	HasReasoning          bool
	HasCompaction         bool
	HasPreviousResponseID bool
	ItemFingerprints      []string
}

func (s stateSignals) hasRouteBoundState() bool {
	return s.HasReasoning || s.HasCompaction || s.HasPreviousResponseID
}

type payloadInspection struct {
	Raw        []byte
	Root       map[string]json.RawMessage
	InputItems []json.RawMessage
	InputArray bool
	Signals    stateSignals
}

type sanitizeStats struct {
	ReasoningRemoved          int
	CompactionRemoved         int
	PreviousResponseIDRemoved bool
}

func (s sanitizeStats) changed() bool {
	return s.ReasoningRemoved > 0 || s.CompactionRemoved > 0 || s.PreviousResponseIDRemoved
}

func inspectPayloadWithDigester(raw []byte, digester opaqueDigester) (payloadInspection, error) {
	inspection := payloadInspection{Raw: raw}
	if len(bytes.TrimSpace(raw)) == 0 {
		return inspection, fmt.Errorf("request body is empty")
	}
	if errUnmarshal := json.Unmarshal(raw, &inspection.Root); errUnmarshal != nil {
		return inspection, fmt.Errorf("decode JSON body: %w", errUnmarshal)
	}
	if inspection.Root == nil {
		return inspection, fmt.Errorf("request body must be a JSON object")
	}
	if _, exists := inspection.Root["previous_response_id"]; exists {
		inspection.Signals.HasPreviousResponseID = true
	}

	input, exists := inspection.Root["input"]
	if !exists || len(bytes.TrimSpace(input)) == 0 || bytes.Equal(bytes.TrimSpace(input), []byte("null")) {
		return inspection, nil
	}
	if errUnmarshal := json.Unmarshal(input, &inspection.InputItems); errUnmarshal != nil {
		return inspection, nil
	}
	inspection.InputArray = true
	for _, item := range inspection.InputItems {
		kind, fingerprint, ok := routeBoundInputItemWithDigester(item, digester)
		if !ok {
			continue
		}
		switch kind {
		case "reasoning":
			inspection.Signals.HasReasoning = true
		case "compaction":
			inspection.Signals.HasCompaction = true
		}
		inspection.Signals.ItemFingerprints = append(inspection.Signals.ItemFingerprints, fingerprint)
	}
	return inspection, nil
}

func routeBoundInputItemWithDigester(raw json.RawMessage, digester opaqueDigester) (string, string, bool) {
	var item struct {
		Type             string          `json:"type"`
		ID               string          `json:"id"`
		EncryptedContent json.RawMessage `json:"encrypted_content"`
	}
	if errUnmarshal := json.Unmarshal(raw, &item); errUnmarshal != nil {
		return "", "", false
	}
	kind := strings.ToLower(strings.TrimSpace(item.Type))
	if kind != "reasoning" && kind != "compaction" {
		return "", "", false
	}
	if id := strings.TrimSpace(item.ID); id != "" {
		return kind, digester.digest("route-bound-item-v1", kind, "id", id), true
	}
	var encryptedContent string
	if errUnmarshal := json.Unmarshal(item.EncryptedContent, &encryptedContent); errUnmarshal == nil && encryptedContent != "" {
		return kind, digester.digest("route-bound-item-v1", kind, "encrypted-content", encryptedContent), true
	}

	// encoding/json emits deterministic object-key ordering, so this fallback
	// remains stable if the client reserializes an otherwise identical item.
	var canonical any
	if errUnmarshal := json.Unmarshal(raw, &canonical); errUnmarshal == nil {
		if encoded, errMarshal := json.Marshal(canonical); errMarshal == nil {
			raw = encoded
		}
	}
	return kind, digester.digest("route-bound-item-v1", kind, "canonical-item", string(raw)), true
}

// stripRouteBoundStateWithStats deliberately changes only top-level Responses
// input items. It never recursively searches for encrypted_content, because
// nested agent messages and tool payloads are independent application data.
func (p payloadInspection) stripRouteBoundStateWithStats() ([]byte, sanitizeStats, error) {
	stats := sanitizeStats{}
	if !p.Signals.hasRouteBoundState() {
		return p.Raw, stats, nil
	}
	if _, exists := p.Root["previous_response_id"]; exists {
		delete(p.Root, "previous_response_id")
		stats.PreviousResponseIDRemoved = true
	}
	if p.InputArray {
		filtered := make([]json.RawMessage, 0, len(p.InputItems))
		for _, item := range p.InputItems {
			var kind struct {
				Type string `json:"type"`
			}
			if errUnmarshal := json.Unmarshal(item, &kind); errUnmarshal != nil {
				filtered = append(filtered, item)
				continue
			}
			switch strings.ToLower(strings.TrimSpace(kind.Type)) {
			case "reasoning":
				stats.ReasoningRemoved++
				continue
			case "compaction":
				stats.CompactionRemoved++
				continue
			default:
				filtered = append(filtered, item)
			}
		}
		if stats.changed() {
			rawInput, errMarshal := json.Marshal(filtered)
			if errMarshal != nil {
				return nil, sanitizeStats{}, fmt.Errorf("encode filtered input: %w", errMarshal)
			}
			p.Root["input"] = rawInput
		}
	}
	if !stats.changed() {
		return p.Raw, stats, nil
	}
	updated, errMarshal := json.Marshal(p.Root)
	if errMarshal != nil {
		return nil, sanitizeStats{}, fmt.Errorf("encode sanitized request: %w", errMarshal)
	}
	return updated, stats, nil
}

// routeBoundRemovalStats reports what a strip decision would remove without
// changing the payload. It lets block-mode responses state the exact impact.
func (p payloadInspection) routeBoundRemovalStatsWithDigester(digester opaqueDigester, fingerprints []string, full bool) sanitizeStats {
	stats := sanitizeStats{}
	if full {
		if _, exists := p.Root["previous_response_id"]; exists {
			stats.PreviousResponseIDRemoved = true
		}
		if !p.InputArray {
			return stats
		}
		for _, item := range p.InputItems {
			kind, _, ok := routeBoundInputItemWithDigester(item, digester)
			if !ok {
				continue
			}
			switch kind {
			case "reasoning":
				stats.ReasoningRemoved++
			case "compaction":
				stats.CompactionRemoved++
			}
		}
		return stats
	}

	if !p.InputArray || len(fingerprints) == 0 {
		return stats
	}
	blocked := make(map[string]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint != "" {
			blocked[fingerprint] = struct{}{}
		}
	}
	for _, item := range p.InputItems {
		kind, fingerprint, ok := routeBoundInputItemWithDigester(item, digester)
		if !ok {
			continue
		}
		if _, remove := blocked[fingerprint]; !remove {
			continue
		}
		switch kind {
		case "reasoning":
			stats.ReasoningRemoved++
		case "compaction":
			stats.CompactionRemoved++
		}
	}
	return stats
}

// stripRetiredRouteBoundItemsWithDigester removes only top-level items already
// observed on a foreign or unknown route. previous_response_id remains intact
// because it belongs to the currently selected route on same-route
// continuations.
func (p payloadInspection) stripRetiredRouteBoundItemsWithDigester(digester opaqueDigester, fingerprints []string) ([]byte, sanitizeStats, error) {
	stats := sanitizeStats{}
	if !p.InputArray || len(fingerprints) == 0 {
		return p.Raw, stats, nil
	}
	blocked := make(map[string]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint != "" {
			blocked[fingerprint] = struct{}{}
		}
	}
	if len(blocked) == 0 {
		return p.Raw, stats, nil
	}

	filtered := make([]json.RawMessage, 0, len(p.InputItems))
	for _, item := range p.InputItems {
		kind, fingerprint, ok := routeBoundInputItemWithDigester(item, digester)
		if ok {
			if _, remove := blocked[fingerprint]; remove {
				if kind == "reasoning" {
					stats.ReasoningRemoved++
				} else if kind == "compaction" {
					stats.CompactionRemoved++
				}
				continue
			}
		}
		filtered = append(filtered, item)
	}
	if !stats.changed() {
		return p.Raw, stats, nil
	}
	rawInput, errMarshal := json.Marshal(filtered)
	if errMarshal != nil {
		return nil, sanitizeStats{}, fmt.Errorf("encode selectively filtered input: %w", errMarshal)
	}
	p.Root["input"] = rawInput
	updated, errMarshal := json.Marshal(p.Root)
	if errMarshal != nil {
		return nil, sanitizeStats{}, fmt.Errorf("encode selectively sanitized request: %w", errMarshal)
	}
	return updated, stats, nil
}

func likelyContainsRouteBoundState(raw []byte) bool {
	lower := bytes.ToLower(raw)
	return bytes.Contains(lower, []byte("\"previous_response_id\"")) ||
		bytes.Contains(lower, []byte("\"reasoning\"")) ||
		bytes.Contains(lower, []byte("\"compaction\""))
}

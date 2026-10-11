package main

import (
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type pluginMetrics struct {
	startedAt time.Time

	requestsObserved     atomic.Uint64
	passedClean          atomic.Uint64
	passedSameRoute      atomic.Uint64
	routeStateStripped   atomic.Uint64
	retiredItemsStripped atomic.Uint64
	compactionsBlocked   atomic.Uint64
	unparseableRejected  atomic.Uint64
	unparseablePassed    atomic.Uint64
	sanitizeFailures     atomic.Uint64
	completedSucceeded   atomic.Uint64
	completedFailed      atomic.Uint64
	completedRejected    atomic.Uint64
	completedCanceled    atomic.Uint64
	completedUnknown     atomic.Uint64
}

type protectionCounters struct {
	RequestsObserved     uint64 `json:"requests_observed"`
	PassedClean          uint64 `json:"passed_clean"`
	PassedSameRoute      uint64 `json:"passed_same_route"`
	RouteStateStripped   uint64 `json:"route_state_stripped"`
	RetiredItemsStripped uint64 `json:"retired_items_stripped"`
	CompactionsBlocked   uint64 `json:"compactions_blocked"`
	UnparseableRejected  uint64 `json:"unparseable_rejected"`
	UnparseablePassed    uint64 `json:"unparseable_passed"`
	SanitizeFailures     uint64 `json:"sanitize_failures"`
	CompletedSucceeded   uint64 `json:"completed_succeeded"`
	CompletedFailed      uint64 `json:"completed_failed"`
	CompletedRejected    uint64 `json:"completed_rejected"`
	CompletedCanceled    uint64 `json:"completed_canceled"`
	CompletedUnknown     uint64 `json:"completed_unknown"`
}

func newPluginMetrics() *pluginMetrics {
	return &pluginMetrics{startedAt: time.Now()}
}

func (m *pluginMetrics) recordCompletion(outcome pluginapi.RequestCompletionOutcome) {
	if m == nil {
		return
	}
	switch outcome {
	case pluginapi.RequestCompletionSucceeded:
		m.completedSucceeded.Add(1)
	case pluginapi.RequestCompletionFailed:
		m.completedFailed.Add(1)
	case pluginapi.RequestCompletionRejected:
		m.completedRejected.Add(1)
	case pluginapi.RequestCompletionCanceled:
		m.completedCanceled.Add(1)
	default:
		m.completedUnknown.Add(1)
	}
}

func (m *pluginMetrics) snapshot() protectionCounters {
	if m == nil {
		return protectionCounters{}
	}
	return protectionCounters{
		RequestsObserved:     m.requestsObserved.Load(),
		PassedClean:          m.passedClean.Load(),
		PassedSameRoute:      m.passedSameRoute.Load(),
		RouteStateStripped:   m.routeStateStripped.Load(),
		RetiredItemsStripped: m.retiredItemsStripped.Load(),
		CompactionsBlocked:   m.compactionsBlocked.Load(),
		UnparseableRejected:  m.unparseableRejected.Load(),
		UnparseablePassed:    m.unparseablePassed.Load(),
		SanitizeFailures:     m.sanitizeFailures.Load(),
		CompletedSucceeded:   m.completedSucceeded.Load(),
		CompletedFailed:      m.completedFailed.Load(),
		CompletedRejected:    m.completedRejected.Load(),
		CompletedCanceled:    m.completedCanceled.Load(),
		CompletedUnknown:     m.completedUnknown.Load(),
	}
}

func (m *pluginMetrics) uptime(now time.Time) time.Duration {
	if m == nil || m.startedAt.IsZero() || now.Before(m.startedAt) {
		return 0
	}
	return now.Sub(m.startedAt)
}

package main

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkRouteStatePrepareWithManyPending(b *testing.B) {
	cfg := testConfig()
	cfg.MaxSessions = 16384
	cfg.MaxPending = 16384
	cfg.StateTTL = 24 * time.Hour
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	store := newRouteStateStoreWithClock(cfg, func() time.Time { return now })
	for index := 0; index < 8192; index++ {
		store.setPendingLocked(fmt.Sprintf("pending-%05d", index), pendingRoute{
			SessionKey: fmt.Sprintf("session-%05d", index),
			Route:      routeFingerprint{Digest: "route-a"},
			RouteKnown: true,
			ObservedAt: now,
		})
	}
	route := routeFingerprint{Digest: "route-target"}

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		store.prepare("benchmark-request", "target-session", route, true, stateSignals{})
	}
}

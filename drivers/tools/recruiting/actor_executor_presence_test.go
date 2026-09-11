package recruiting

import (
	"testing"
	"time"

	"github.com/wanpengxie/atoll/lib/introspect"
)

func TestExecutorPresenceObservationOnlySweepsTrustedEdges(t *testing.T) {
	state := &storedState{ExecutorPresence: map[string]executorPresenceObservation{
		"tool:presence-http:7": {}, "tool:presence-browser:9": {},
	}}
	firstAt := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC)
	first := introspect.Catalog{Actors: []introspect.CatalogEntry{
		{ID: "tool:presence-http:7", Present: true, UptimeMs: 100_000},
		{ID: "tool:presence-browser:9", Present: false},
		{ID: "tool:not-configured:1", Present: false},
	}}
	var result executorPresenceReconcileResult
	observeExecutorPresence(state, first, firstAt, &result)
	if result.MembersObserved != 2 || len(state.ExecutorPresence) != 2 {
		t.Fatalf("initial presence result=%+v state=%+v", result, state.ExecutorPresence)
	}
	present := state.ExecutorPresence["tool:presence-http:7"]
	absent := state.ExecutorPresence["tool:presence-browser:9"]
	if present.SweepReason != "executor_incarnation_replaced" ||
		present.SweepBefore != firstAt.Add(-101*time.Second).Format(time.RFC3339Nano) ||
		absent.SweepReason != "executor_not_present" || absent.SweepBefore != firstAt.Format(time.RFC3339Nano) {
		t.Fatalf("initial trusted sweeps present=%+v absent=%+v", present, absent)
	}

	present.SweepBefore, present.SweepReason = "", ""
	absent.SweepBefore, absent.SweepReason = "", ""
	state.ExecutorPresence["tool:presence-http:7"] = present
	state.ExecutorPresence["tool:presence-browser:9"] = absent
	secondAt := firstAt.Add(30 * time.Second)
	result = executorPresenceReconcileResult{}
	observeExecutorPresence(state, introspect.Catalog{Actors: []introspect.CatalogEntry{
		{ID: "tool:presence-http:7", Present: true, UptimeMs: 130_000},
		{ID: "tool:presence-browser:9", Present: false},
	}}, secondAt, &result)
	if state.ExecutorPresence["tool:presence-http:7"].SweepBefore != "" ||
		state.ExecutorPresence["tool:presence-browser:9"].SweepBefore != "" {
		t.Fatalf("steady presence created repeated sweeps: %+v", state.ExecutorPresence)
	}

	thirdAt := secondAt.Add(10 * time.Second)
	observeExecutorPresence(state, introspect.Catalog{Actors: []introspect.CatalogEntry{
		{ID: "tool:presence-http:7", Present: true, UptimeMs: 500},
		{ID: "tool:presence-browser:9", Present: true, UptimeMs: 200},
	}}, thirdAt, &executorPresenceReconcileResult{})
	if got := state.ExecutorPresence["tool:presence-http:7"]; got.SweepReason != "executor_incarnation_replaced" ||
		got.SweepBefore != thirdAt.Add(-1500*time.Millisecond).Format(time.RFC3339Nano) {
		t.Fatalf("uptime reset was not fenced: %+v", got)
	}
	if got := state.ExecutorPresence["tool:presence-browser:9"]; got.SweepReason != "executor_incarnation_replaced" {
		t.Fatalf("offline-to-online bind was not fenced: %+v", got)
	}

	present = state.ExecutorPresence["tool:presence-http:7"]
	present.SweepBefore, present.SweepReason = "", ""
	state.ExecutorPresence["tool:presence-http:7"] = present
	fourthAt := thirdAt.Add(time.Second)
	observeExecutorPresence(state, introspect.Catalog{Actors: []introspect.CatalogEntry{
		{ID: "tool:presence-browser:9", Present: true, UptimeMs: 1_200},
	}}, fourthAt, &executorPresenceReconcileResult{})
	if got := state.ExecutorPresence["tool:presence-http:7"]; got.Present || got.SweepReason != "executor_not_present" ||
		got.SweepBefore != fourthAt.Format(time.RFC3339Nano) {
		t.Fatalf("member disappearance was not fenced: %+v", got)
	}
}

func TestExecutorSweepRotationStartsAfterLastActor(t *testing.T) {
	values := []string{"a", "b", "c"}
	for cursor, want := range map[string][]string{
		"": {"a", "b", "c"}, "a": {"b", "c", "a"}, "b": {"c", "a", "b"}, "c": {"a", "b", "c"},
	} {
		got := rotateAfter(values, cursor)
		if len(got) != len(want) {
			t.Fatalf("rotate %q=%v", cursor, got)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("rotate %q=%v want=%v", cursor, got, want)
			}
		}
	}
}

func TestExecutorPresenceTreatsInitiallyMissingTrackedActorAsAbsent(t *testing.T) {
	state := &storedState{ExecutorPresence: map[string]executorPresenceObservation{
		"tool:missing-http:7": {},
	}}
	at := time.Date(2026, 9, 11, 2, 0, 0, 0, time.UTC)
	observeExecutorPresence(state, introspect.Catalog{}, at, &executorPresenceReconcileResult{})
	got := state.ExecutorPresence["tool:missing-http:7"]
	if got.Present || got.SweepReason != "executor_not_present" || got.SweepBefore != at.Format(time.RFC3339Nano) {
		t.Fatalf("initial catalog disappearance was not fenced: %+v", got)
	}
}

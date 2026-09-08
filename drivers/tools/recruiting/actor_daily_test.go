package recruiting

import (
	"testing"
	"time"

	"github.com/wanpengxie/atoll/protocol/message"
)

func TestNextDailyCutoffPreservesLocalDateAcrossDST(t *testing.T) {
	cfg := defaultConfig()
	cfg.DailyScheduleTimezone = "America/New_York"
	cfg.DailyCutoffLocal = "01:30:00"
	cfg.DailyWindowStartDelayMinutes = 30
	cfg.DailyWindowDurationMinutes = 480
	cfg.DailySchedulePolicyVersion = 9

	payload, cutoff, err := nextDailyCutoff(cfg, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if payload.ScheduleDate != "2026-03-08" || cutoff != time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC) ||
		payload.Schedule.PolicyVersion != 9 || payload.Schedule.WindowStartAt != "2026-03-08T07:00:00Z" ||
		payload.Schedule.WindowEndAt != "2026-03-08T15:00:00Z" {
		t.Fatalf("DST cutoff = %+v at %s", payload, cutoff)
	}

	next, nextCutoff, err := nextDailyCutoff(cfg, cutoff)
	if err != nil || next.ScheduleDate != "2026-03-09" || !nextCutoff.After(cutoff) {
		t.Fatalf("next cutoff = %+v at %s err=%v", next, nextCutoff, err)
	}
}

func TestOnlyPersistedDailyTimerCanCreateOrGrowTheChain(t *testing.T) {
	if !isCurrentDailyTimer(message.ID("timer:daily-current"), "daily-current") {
		t.Fatal("persisted daily timer fire was rejected")
	}
	for _, id := range []message.ID{"daily-current", "timer:daily-orphan", "timer:"} {
		if isCurrentDailyTimer(id, "daily-current") {
			t.Fatalf("stale daily timer %q was accepted", id)
		}
	}
}

func TestOnlyPersistedDailyWorkTimerCanMaterializeOrGrowTheChain(t *testing.T) {
	if !isCurrentDurableTimer(message.ID("timer:work-current"), "work-current") {
		t.Fatal("persisted daily work timer fire was rejected")
	}
	for _, id := range []message.ID{"work-current", "timer:work-orphan", "timer:"} {
		if isCurrentDurableTimer(id, "work-current") {
			t.Fatalf("stale daily work timer %q was accepted", id)
		}
	}
}

func TestOnlyPersistedDailyCloseTimerCanSettleOrGrowTheChain(t *testing.T) {
	if !isCurrentDurableTimer(message.ID("timer:close-current"), "close-current") {
		t.Fatal("persisted daily close timer fire was rejected")
	}
	for _, id := range []message.ID{"close-current", "timer:close-orphan", "timer:"} {
		if isCurrentDurableTimer(id, "close-current") {
			t.Fatalf("stale daily close timer %q was accepted", id)
		}
	}
}

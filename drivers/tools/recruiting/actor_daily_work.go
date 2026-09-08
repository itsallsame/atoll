package recruiting

import (
	"context"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

const typeDailyWorkDue = "recruiting.daily.work.due"

type dailyWorkDuePayload struct {
	Reason string `json:"reason"`
}

// armNextDailyWorkTimer uses the earliest persisted due_at as its clock. It
// creates no periodic polling loop when no work is due or planned.
func armNextDailyWorkTimer(sys actorbase.Sys, cfg Config, state *storedState, repository *store.Repository, now time.Time) error {
	dueAt, found, err := repository.NextPlannedOccurrenceDueAt(context.Background())
	if err != nil {
		return err
	}
	if !found {
		if state.DailyWorkTimerID == "" {
			return nil
		}
		state.DailyWorkTimerID = ""
		return persist(sys, state)
	}
	delay := dueAt.Sub(now)
	if delay <= 0 {
		delay = time.Millisecond
	}
	timerID, err := sys.After(delay, typeDailyWorkDue, dailyWorkDuePayload{Reason: "source_occurrence_due"}, schedule.TimerHomeDurable)
	if err != nil {
		return fmt.Errorf("arm recruiting daily work: %w", err)
	}
	previous := state.DailyWorkTimerID
	state.DailyWorkTimerID = string(timerID)
	if err := persist(sys, state); err != nil {
		state.DailyWorkTimerID = previous
		_ = sys.CancelTimer(timerID)
		return err
	}
	return nil
}

func handleDailyWorkDue(sys actorbase.Sys, cfg Config, state *storedState, repository *store.Repository, msg actorbase.Msg) error {
	if !isCurrentDurableTimer(msg.ID, state.DailyWorkTimerID) {
		return nil
	}
	if !cfg.DailyScheduleEnabled {
		state.DailyWorkTimerID = ""
		return persist(sys, state)
	}
	if repository == nil {
		return fmt.Errorf("recruiting daily work database is not configured")
	}
	now := time.Now().UTC()
	if _, err := repository.MaterializeDueOccurrenceWorksWithDispatch(msg.Ctx(), now, cfg.DailyWorkMaterializeLimit,
		string(sys.Self()), string(msg.ID), now, cfg.executionDispatchTargets()); err != nil {
		return fmt.Errorf("materialize recruiting daily work: %w", err)
	}
	return armNextDailyWorkTimer(sys, cfg, state, repository, time.Now().UTC())
}

package recruiting

import (
	"context"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

const typeDailyCloseDue = "recruiting.daily.close.due"

type dailyCloseDuePayload struct {
	Reason string `json:"reason"`
}

func armNextDailyCloseTimer(sys actorbase.Sys, cfg Config, state *storedState, repository *store.Repository, now time.Time) error {
	windowEnd, found, err := repository.NextRunningDailyWindowEnd(context.Background())
	if err != nil {
		return err
	}
	if !found {
		if state.DailyCloseTimerID == "" {
			return nil
		}
		state.DailyCloseTimerID = ""
		return persist(sys, state)
	}
	delay := windowEnd.Sub(now)
	if delay <= 0 {
		delay = time.Millisecond
	}
	timerID, err := sys.After(delay, typeDailyCloseDue, dailyCloseDuePayload{Reason: "daily_window_end"}, schedule.TimerHomeDurable)
	if err != nil {
		return fmt.Errorf("arm recruiting daily close: %w", err)
	}
	previous := state.DailyCloseTimerID
	state.DailyCloseTimerID = string(timerID)
	if err := persist(sys, state); err != nil {
		state.DailyCloseTimerID = previous
		_ = sys.CancelTimer(timerID)
		return err
	}
	return nil
}

func handleDailyCloseDue(sys actorbase.Sys, cfg Config, state *storedState, repository *store.Repository, msg actorbase.Msg) error {
	if !isCurrentDurableTimer(msg.ID, state.DailyCloseTimerID) {
		return nil
	}
	if !cfg.DailyScheduleEnabled {
		state.DailyCloseTimerID = ""
		return persist(sys, state)
	}
	if repository == nil {
		return fmt.Errorf("recruiting daily close database is not configured")
	}
	now := time.Now().UTC()
	if _, err := repository.CloseDueDailyRuns(msg.Ctx(), now, string(msg.ID)); err != nil {
		return fmt.Errorf("close recruiting daily runs: %w", err)
	}
	state.DailyCloseTimerID = ""
	if err := persist(sys, state); err != nil {
		return err
	}
	return armNextDailyCloseTimer(sys, cfg, state, repository, time.Now().UTC())
}

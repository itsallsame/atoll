package recruiting

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/message"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

const typeDailyCutoffDue = "recruiting.daily.cutoff.due"

type dailyCutoffDuePayload struct {
	DailyRunID   string              `json:"daily_run_id"`
	ScheduleDate string              `json:"schedule_date"`
	Schedule     model.DailySchedule `json:"schedule"`
	EventID      string              `json:"event_id"`
}

func nextDailyCutoff(cfg Config, after time.Time) (dailyCutoffDuePayload, time.Time, error) {
	location, err := time.LoadLocation(cfg.DailyScheduleTimezone)
	if err != nil {
		return dailyCutoffDuePayload{}, time.Time{}, err
	}
	clock, err := time.Parse("15:04:05", cfg.DailyCutoffLocal)
	if err != nil {
		return dailyCutoffDuePayload{}, time.Time{}, err
	}
	local := after.In(location)
	cutoff := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), clock.Second(), 0, location)
	if !cutoff.After(local) {
		cutoff = time.Date(local.Year(), local.Month(), local.Day()+1, clock.Hour(), clock.Minute(), clock.Second(), 0, location)
	}
	scheduleDate := cutoff.Format("2006-01-02")
	cutoffUTC := cutoff.UTC()
	windowStart := cutoffUTC.Add(time.Duration(cfg.DailyWindowStartDelayMinutes) * time.Minute)
	windowEnd := windowStart.Add(time.Duration(cfg.DailyWindowDurationMinutes) * time.Minute)
	payload := dailyCutoffDuePayload{
		DailyRunID: "daily-run-" + scheduleDate, ScheduleDate: scheduleDate,
		Schedule: model.DailySchedule{
			PolicyVersion: cfg.DailySchedulePolicyVersion, CutoffAt: cutoffUTC.Format(time.RFC3339Nano),
			WindowStartAt: windowStart.Format(time.RFC3339Nano), WindowEndAt: windowEnd.Format(time.RFC3339Nano),
		},
		EventID: "daily-run-started-" + scheduleDate,
	}
	return payload, cutoffUTC, nil
}

func armDailyTimer(sys actorbase.Sys, cfg Config, state *storedState, now time.Time) error {
	payload, cutoffAt, err := nextDailyCutoff(cfg, now)
	if err != nil {
		return fmt.Errorf("compute recruiting daily cutoff: %w", err)
	}
	delay := cutoffAt.Sub(now)
	if delay <= 0 {
		return fmt.Errorf("computed recruiting daily cutoff is not in the future")
	}
	timerID, err := sys.After(delay, typeDailyCutoffDue, payload, schedule.TimerHomeDurable)
	if err != nil {
		return fmt.Errorf("arm recruiting daily cutoff: %w", err)
	}
	previous := state.DailyTimerID
	state.DailyTimerID = string(timerID)
	if err := persist(sys, state); err != nil {
		state.DailyTimerID = previous
		_ = sys.CancelTimer(timerID)
		return err
	}
	return nil
}

func handleDailyCutoffDue(sys actorbase.Sys, cfg Config, state *storedState, repository *store.Repository, msg actorbase.Msg) error {
	if !isCurrentDurableTimer(msg.ID, state.DailyTimerID) {
		return nil
	}
	if !cfg.DailyScheduleEnabled {
		state.DailyTimerID = ""
		return persist(sys, state)
	}
	if repository == nil {
		return fmt.Errorf("recruiting daily cutoff database is not configured")
	}
	var payload dailyCutoffDuePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return fmt.Errorf("decode recruiting daily cutoff: %w", err)
	}
	_, err := repository.PlanDailyRunAtCutoff(msg.Ctx(), store.DailyPlanRequest{
		DailyRunID: payload.DailyRunID, ScheduleDate: payload.ScheduleDate, Schedule: payload.Schedule,
		TriggerID: string(msg.ID), EventID: payload.EventID,
	}, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("plan recruiting daily cutoff: %w", err)
	}
	if state.DailyWorkTimerID == "" {
		if err := armNextDailyWorkTimer(sys, cfg, state, repository, time.Now().UTC()); err != nil {
			return err
		}
	}
	return armDailyTimer(sys, cfg, state, time.Now().UTC())
}

func isCurrentDailyTimer(messageID message.ID, currentTimerID string) bool {
	return isCurrentDurableTimer(messageID, currentTimerID)
}

package recruiting

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/introspect"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

const (
	executorPresenceWait       = 2 * time.Second
	executorBindEstimateMargin = time.Second
)

type executorPresenceObservation struct {
	Present     bool   `json:"present"`
	UptimeMS    int64  `json:"uptime_ms,omitempty"`
	ObservedAt  string `json:"observed_at"`
	SweepBefore string `json:"sweep_before,omitempty"`
	SweepReason string `json:"sweep_reason,omitempty"`
}

type executorPresenceReconcileResult struct {
	MembersObserved  int `json:"executor_members_observed"`
	SweepsAttempted  int `json:"executor_sweeps_attempted"`
	AttemptsScanned  int `json:"executor_attempts_scanned"`
	AttemptsExpired  int `json:"executor_attempts_expired"`
	WorksRetryQueued int `json:"executor_works_retry_queued"`
	Conflicts        int `json:"executor_presence_conflicts"`
}

func reconcileExecutorPresence(sys actorbase.Sys, cfg Config, state *storedState, repository *store.Repository,
	now time.Time) (executorPresenceReconcileResult, error) {
	calledAt := now.UTC()
	pending, err := sys.Call(message.Root(), actor.SystemActorID, message.TypeSystemMemberList, struct{}{})
	if err != nil {
		return executorPresenceReconcileResult{}, fmt.Errorf("query executor presence: %w", err)
	}
	response, err := pending.Wait(sys.Life(), executorPresenceWait)
	if err != nil {
		_ = pending.Cancel()
		return executorPresenceReconcileResult{}, fmt.Errorf("wait executor presence: %w", err)
	}
	if response.Kind != message.KindResponse || response.Type != message.TypeSystemMemberList {
		return executorPresenceReconcileResult{}, fmt.Errorf("executor presence returned an unrelated response")
	}
	var wire struct {
		Status string `json:"status"`
		introspect.Catalog
	}
	if err := json.Unmarshal(response.Payload, &wire); err != nil || wire.Status != message.StatusCompleted {
		return executorPresenceReconcileResult{}, fmt.Errorf("executor presence returned an invalid terminal")
	}
	result := executorPresenceReconcileResult{}
	trackConfiguredExecutorMembers(state, wire.Catalog, cfg.Executors)
	observeExecutorPresence(state, wire.Catalog, calledAt, &result)
	if err := sweepInvalidExecutorAttempts(sys.Life(), state, repository, cfg.AttemptRecoveryLimit, now.UTC(), &result); err != nil {
		return result, err
	}
	return result, nil
}

// trackConfiguredExecutorMembers adds the current concrete incarnation of a
// configured fleet target before presence transitions are evaluated. Without
// this, a dispatch posted before that Executor ever claimed Work can inherit a
// long acknowledgement deadline while the daemon is offline, and its later
// arrival cannot accelerate the wake. Concrete IDs remain the authority for
// attempt fencing; the configured base ID is only a bounded discovery hint.
func trackConfiguredExecutorMembers(state *storedState, catalog introspect.Catalog, targets []ExecutorTargetConfig) {
	if len(targets) == 0 || len(catalog.Actors) == 0 {
		return
	}
	bases := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		bases[string(target.ActorID)] = struct{}{}
	}
	for _, member := range catalog.Actors {
		if _, tracked := state.ExecutorPresence[member.ID]; tracked {
			continue
		}
		_, configured := bases[member.ID]
		if !configured {
			if separator := strings.LastIndexByte(member.ID, ':'); separator > 0 {
				_, configured = bases[member.ID[:separator]]
			}
		}
		if configured {
			state.ExecutorPresence[member.ID] = executorPresenceObservation{}
		}
	}
}

func observeExecutorPresence(state *storedState, catalog introspect.Catalog, observedAt time.Time,
	result *executorPresenceReconcileResult) {
	seen := make(map[string]struct{})
	for _, member := range catalog.Actors {
		if _, tracked := state.ExecutorPresence[member.ID]; !tracked {
			continue
		}
		seen[member.ID] = struct{}{}
		result.MembersObserved++
		previous, known := state.ExecutorPresence[member.ID]
		current := executorPresenceObservation{Present: member.Present, UptimeMS: member.UptimeMs,
			ObservedAt: observedAt.UTC().Format(time.RFC3339Nano)}
		switch {
		case !member.Present:
			if known && previous.ObservedAt != "" && !previous.Present {
				current.SweepBefore, current.SweepReason = previous.SweepBefore, previous.SweepReason
			} else {
				current.SweepBefore = observedAt.UTC().Format(time.RFC3339Nano)
				current.SweepReason = "executor_not_present"
			}
		case !known || !previous.Present || member.UptimeMs+executorBindEstimateMargin.Milliseconds() < previous.UptimeMS:
			bindLowerBound := observedAt.Add(-time.Duration(member.UptimeMs)*time.Millisecond - executorBindEstimateMargin)
			current.SweepBefore = bindLowerBound.UTC().Format(time.RFC3339Nano)
			current.SweepReason = "executor_incarnation_replaced"
		default:
			current.SweepBefore, current.SweepReason = previous.SweepBefore, previous.SweepReason
		}
		state.ExecutorPresence[member.ID] = current
	}
	for memberID, previous := range state.ExecutorPresence {
		if _, present := seen[memberID]; present {
			continue
		}
		if !previous.Present && previous.ObservedAt != "" && previous.SweepBefore == "" {
			delete(state.ExecutorPresence, memberID)
			continue
		}
		wasPresent := previous.Present
		wasUnknown := previous.ObservedAt == ""
		previous.Present = false
		previous.UptimeMS = 0
		previous.ObservedAt = observedAt.UTC().Format(time.RFC3339Nano)
		if wasUnknown || wasPresent {
			previous.SweepBefore = observedAt.UTC().Format(time.RFC3339Nano)
			previous.SweepReason = "executor_not_present"
		}
		state.ExecutorPresence[memberID] = previous
	}
}

func sweepInvalidExecutorAttempts(ctx context.Context, state *storedState, repository *store.Repository, limit int,
	recoveredAt time.Time, result *executorPresenceReconcileResult) error {
	if limit < 1 {
		return nil
	}
	pendingIDs := make([]string, 0, len(state.ExecutorPresence))
	for memberID, observation := range state.ExecutorPresence {
		if observation.SweepBefore != "" {
			pendingIDs = append(pendingIDs, memberID)
		}
	}
	sort.Strings(pendingIDs)
	pendingIDs = rotateAfter(pendingIDs, state.ExecutorSweepCursor)
	remaining := limit
	sweepsAttempted := 0
	for index, memberID := range pendingIDs {
		if remaining == 0 || sweepsAttempted == limit {
			break
		}
		observation := state.ExecutorPresence[memberID]
		before, err := time.Parse(time.RFC3339Nano, observation.SweepBefore)
		if err != nil {
			return fmt.Errorf("decode executor sweep boundary: %w", err)
		}
		actorsLeft := len(pendingIDs) - index
		share := remaining / actorsLeft
		if share < 1 {
			share = 1
		}
		recovery, err := repository.RecoverExecutorAttempts(ctx, memberID, before, share,
			recoveredAt, observation.SweepReason)
		if err != nil {
			return err
		}
		result.SweepsAttempted++
		sweepsAttempted++
		result.AttemptsScanned += recovery.Scanned
		result.AttemptsExpired += recovery.Expired
		result.WorksRetryQueued += recovery.RetryQueued
		result.Conflicts += recovery.Conflicts
		remaining -= recovery.Scanned
		state.ExecutorSweepCursor = memberID
		if !recovery.HasMore {
			observation.SweepBefore, observation.SweepReason = "", ""
			state.ExecutorPresence[memberID] = observation
		}
	}
	return nil
}

func rotateAfter(values []string, cursor string) []string {
	if len(values) < 2 || cursor == "" {
		return values
	}
	index := sort.SearchStrings(values, cursor)
	for index < len(values) && values[index] <= cursor {
		index++
	}
	if index == len(values) {
		return values
	}
	rotated := make([]string, 0, len(values))
	rotated = append(rotated, values[index:]...)
	rotated = append(rotated, values[:index]...)
	return rotated
}

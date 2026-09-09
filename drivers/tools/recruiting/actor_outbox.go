package recruiting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

const (
	defaultReconcileLimit  = 100
	maxReconcileLimit      = 500
	typeOutboxReconcileDue = "recruiting.outbox.reconcile.due"
)

type outboxReconcilePayload struct {
	Limit int `json:"limit,omitempty"`
}

type outboxReconcileResponse struct {
	ContractVersion       string `json:"contract_version"`
	Scanned               int    `json:"scanned"`
	Delivered             int    `json:"delivered"`
	RetryScheduled        int    `json:"retry_scheduled"`
	Exhausted             int    `json:"exhausted"`
	Conflicts             int    `json:"conflicts"`
	CheckpointError       int    `json:"checkpoint_error"`
	AttemptsScanned       int    `json:"attempts_scanned"`
	AttemptsExpired       int    `json:"attempts_expired"`
	WorksRetryQueued      int    `json:"works_retry_queued"`
	AttemptConflicts      int    `json:"attempt_conflicts"`
	DispatchScanned       int    `json:"dispatch_scanned"`
	DispatchPosted        int    `json:"dispatch_posted"`
	DispatchRetries       int    `json:"dispatch_retries"`
	DispatchExhausted     int    `json:"dispatch_exhausted"`
	BaselineSourceID      string `json:"baseline_source_id,omitempty"`
	BaselineGeneration    uint64 `json:"baseline_generation,omitempty"`
	BaselineMaterialized  int    `json:"baseline_materialized"`
	BaselineDispatches    int    `json:"baseline_dispatches"`
	BaselinePageCompleted bool   `json:"baseline_page_completed"`
}

type outboxReconcileDuePayload struct {
	Reason string `json:"reason"`
}

// handleOutboxReconcile is deliberately a bounded control-plane operation.
// It projects durable recruiting facts into Atoll's ledger, while the stable
// event ID and fingerprint make a crash after Emit but before the SQL delivery
// checkpoint safe to replay.
func handleOutboxReconcile(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload outboxReconcilePayload
	if !decode(sys, msg, &payload) {
		return
	}
	if payload.Limit == 0 {
		payload.Limit = defaultReconcileLimit
	}
	if payload.Limit < 1 || payload.Limit > maxReconcileLimit {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "limit must be in [1,500]")
		return
	}

	now := time.Now().UTC()
	recovery, err := repository.RecoverStaleAttempts(msg.Ctx(), now.Add(-time.Duration(cfg.AttemptStaleAfterMS)*time.Millisecond), cfg.AttemptRecoveryLimit, now)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response, err := reconcileOutbox(msg.Ctx(), sys, repository, payload.Limit, now)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response.AttemptsScanned, response.AttemptsExpired = recovery.Scanned, recovery.Expired
	response.WorksRetryQueued, response.AttemptConflicts = recovery.RetryQueued, recovery.Conflicts
	materialized, err := repository.MaterializeNextBaselinePage(msg.Ctx(), cfg.BaselineMaterializeLimit, now,
		cfg.executionDispatchTargets())
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response.BaselineSourceID, response.BaselineGeneration = materialized.SourceID, materialized.Generation
	response.BaselineMaterialized, response.BaselinePageCompleted = materialized.Processed, materialized.Completed
	response.BaselineDispatches = materialized.Dispatches
	dispatch, err := reconcileExecutionDispatches(msg.Ctx(), sys, repository, payload.Limit, now,
		time.Duration(cfg.AttemptStaleAfterMS)*time.Millisecond)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response.DispatchScanned, response.DispatchPosted = dispatch.Scanned, dispatch.Posted
	response.DispatchRetries, response.DispatchExhausted = dispatch.RetryScheduled, dispatch.Exhausted
	_, _ = sys.Reply(msg, response)
}

type dispatchReconcileResult struct {
	Scanned        int
	Posted         int
	RetryScheduled int
	Exhausted      int
	Conflicts      int
}

func reconcileExecutionDispatches(ctx context.Context, sys actorbase.Sys, repository *store.Repository, limit int,
	now time.Time, completionTimeout time.Duration) (dispatchReconcileResult, error) {
	pending, err := repository.ListPendingExecutionDispatches(ctx, now, limit)
	if err != nil {
		return dispatchReconcileResult{}, err
	}
	result := dispatchReconcileResult{Scanned: len(pending)}
	for _, item := range pending {
		deliveryID := "execution-dispatch-" + stableDigest(item.Intent.DispatchID) + "-" + fmt.Sprintf("%d", item.Attempts+1)
		payload, marshalErr := json.Marshal(executioncontract.WakeRequest{CommandID: item.Intent.DispatchID,
			Origin: item.Intent.Origin, ProfileID: item.Intent.ProfileID})
		if marshalErr != nil {
			return result, marshalErr
		}
		_, postErr := sys.Post(behavior.RequestSpec{ID: message.ID(deliveryID), Type: executioncontract.TypeWake,
			Payload: payload, Audience: message.Audience{actor.ActorID(item.Intent.TargetActorID)}, Cause: message.Root()})
		nextAttemptAt, errorClass := now.Add(completionTimeout), "awaiting_completion"
		if postErr != nil {
			nextAttemptAt = now.Add(outboxRetryDelay(item.Attempts))
			errorClass = classifyOutboxDeliveryError(postErr)
		}
		update, updateErr := repository.RecordExecutionDispatchFailureCAS(ctx, item.Intent.DispatchID, item.Attempts,
			nextAttemptAt, errorClass)
		if errors.Is(updateErr, store.ErrDispatchConflict) {
			result.Conflicts++
			continue
		}
		if updateErr != nil {
			return result, updateErr
		}
		if postErr == nil {
			result.Posted++
		}
		if update.Status == "exhausted" {
			result.Exhausted++
		} else {
			result.RetryScheduled++
		}
	}
	return result, nil
}

func reconcileOutbox(ctx context.Context, sys actorbase.Sys, repository *store.Repository, limit int, now time.Time) (outboxReconcileResponse, error) {
	events, err := repository.ListPendingEvents(ctx, now, limit)
	if err != nil {
		return outboxReconcileResponse{}, err
	}
	response := outboxReconcileResponse{ContractVersion: ContractVersion, Scanned: len(events)}
	for _, pending := range events {
		intent := pending.Intent
		ledgerPayload, marshalErr := json.Marshal(intent)
		if marshalErr != nil {
			response.CheckpointError++
			continue
		}
		_, emitErr := sys.Emit(behavior.EventSpec{
			ID:                message.ID(intent.EventID),
			Type:              intent.Kind,
			Payload:           ledgerPayload,
			Cause:             message.Root(),
			ClientFingerprint: outboxEventFingerprint(intent.EventID, intent.Kind, ledgerPayload),
		})
		if emitErr == nil {
			if err := repository.MarkEventDelivered(ctx, intent.EventID, time.Now().UTC()); err != nil {
				response.CheckpointError++
			} else {
				response.Delivered++
			}
			continue
		}
		update, retryErr := repository.RecordEventFailureCAS(ctx, intent.EventID, pending.Attempts,
			now.Add(outboxRetryDelay(pending.Attempts)), classifyOutboxDeliveryError(emitErr))
		if errors.Is(retryErr, store.ErrOutboxConflict) {
			response.Conflicts++
			continue
		}
		if retryErr != nil {
			response.CheckpointError++
			continue
		}
		if update.Status == "exhausted" {
			response.Exhausted++
		} else {
			response.RetryScheduled++
		}
	}
	return response, nil
}

// armReconcileTimer persists the freshly minted ID before Recv can observe a
// fire. If persistence fails, the new timer is cancelled and actor startup is
// failed instead of running with an untracked recurring chain.
func armReconcileTimer(sys actorbase.Sys, cfg Config, state *storedState) error {
	timerID, err := sys.After(time.Duration(cfg.ReconcileIntervalMS)*time.Millisecond, typeOutboxReconcileDue,
		outboxReconcileDuePayload{Reason: "outbox_delivery"}, schedule.TimerHomeDurable)
	if err != nil {
		return err
	}
	previous := state.ReconcileTimerID
	state.ReconcileTimerID = string(timerID)
	if err := persist(sys, state); err != nil {
		state.ReconcileTimerID = previous
		_ = sys.CancelTimer(timerID)
		return err
	}
	return nil
}

func handleOutboxReconcileDue(sys actorbase.Sys, cfg Config, state *storedState, repository *store.Repository, msg actorbase.Msg) error {
	if !isCurrentReconcileTimer(msg.ID, state.ReconcileTimerID) {
		// A crash after scheduling but before persisting can leave one orphan
		// one-shot timer. It is acknowledged as stale and never grows a chain.
		return nil
	}
	if repository != nil {
		now := time.Now().UTC()
		_, _ = repository.RecoverStaleAttempts(msg.Ctx(), now.Add(-time.Duration(cfg.AttemptStaleAfterMS)*time.Millisecond), cfg.AttemptRecoveryLimit, now)
		_, _ = reconcileOutbox(msg.Ctx(), sys, repository, defaultReconcileLimit, now)
		_, _ = repository.MaterializeNextBaselinePage(msg.Ctx(), cfg.BaselineMaterializeLimit, now,
			cfg.executionDispatchTargets())
		_, _ = reconcileExecutionDispatches(msg.Ctx(), sys, repository, defaultReconcileLimit, now,
			time.Duration(cfg.AttemptStaleAfterMS)*time.Millisecond)
	}
	// Rearm and persist before the raw Proc calls Recv again and acknowledges
	// the current fire. A crash on either side therefore leaves one of the two
	// durable timer IDs as the authoritative chain head.
	return armReconcileTimer(sys, cfg, state)
}

func isCurrentReconcileTimer(messageID message.ID, currentTimerID string) bool {
	return isCurrentDurableTimer(messageID, currentTimerID)
}

func isCurrentDurableTimer(messageID message.ID, currentTimerID string) bool {
	const timerPrefix = "timer:"
	firedTimerID := strings.TrimPrefix(string(messageID), timerPrefix)
	return firedTimerID != string(messageID) && firedTimerID == currentTimerID && currentTimerID != ""
}

func outboxEventFingerprint(eventID, eventKind string, payload []byte) string {
	sum := sha256.Sum256(append([]byte(eventID+"\n"+eventKind+"\n"), payload...))
	return "recruiting-outbox-v1:sha256:" + hex.EncodeToString(sum[:])
}

func outboxRetryDelay(attempts uint64) time.Duration {
	shift := attempts
	if shift > 6 {
		shift = 6
	}
	delay := 5 * time.Second * time.Duration(uint64(1)<<shift)
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func classifyOutboxDeliveryError(err error) string {
	var rejected *actorbase.WriteRejected
	if errors.As(err, &rejected) {
		return "atoll_ledger_rejected"
	}
	if strings.TrimSpace(err.Error()) == "" {
		return "atoll_ledger_error"
	}
	return "atoll_ledger_unavailable"
}

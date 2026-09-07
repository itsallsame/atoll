package recruiting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/protocol/message"
)

const (
	defaultReconcileLimit = 100
	maxReconcileLimit     = 500
)

type outboxReconcilePayload struct {
	Limit int `json:"limit,omitempty"`
}

type outboxReconcileResponse struct {
	ContractVersion string `json:"contract_version"`
	Scanned         int    `json:"scanned"`
	Delivered       int    `json:"delivered"`
	RetryScheduled  int    `json:"retry_scheduled"`
	Exhausted       int    `json:"exhausted"`
	Conflicts       int    `json:"conflicts"`
	CheckpointError int    `json:"checkpoint_error"`
}

// handleOutboxReconcile is deliberately a bounded control-plane operation.
// It projects durable recruiting facts into Atoll's ledger, while the stable
// event ID and fingerprint make a crash after Emit but before the SQL delivery
// checkpoint safe to replay.
func handleOutboxReconcile(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
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
	events, err := repository.ListPendingEvents(msg.Ctx(), now, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
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
			if err := repository.MarkEventDelivered(msg.Ctx(), intent.EventID, time.Now().UTC()); err != nil {
				response.CheckpointError++
			} else {
				response.Delivered++
			}
			continue
		}
		update, retryErr := repository.RecordEventFailureCAS(msg.Ctx(), intent.EventID, pending.Attempts,
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
	_, _ = sys.Reply(msg, response)
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

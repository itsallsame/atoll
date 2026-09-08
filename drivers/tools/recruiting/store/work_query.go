package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type WorkListQuery struct {
	Status           model.WorkStatus
	Purpose          string
	Trigger          string
	WaitingReason    string
	TargetType       string
	TargetID         string
	InitiatorActorID string
	UpdatedFrom      *time.Time
	UpdatedBefore    *time.Time
	Cursor           string
	Limit            int
}

type WorkPage struct {
	Items      []WorkRecord
	NextCursor string
	HasMore    bool
}

type workListCursor struct {
	SelectorHash string `json:"selector_hash"`
	UpdatedAt    string `json:"updated_at"`
	WorkID       string `json:"work_id"`
}

func (r *Repository) ListWorks(ctx context.Context, query WorkListQuery) (WorkPage, error) {
	query.Purpose, query.Trigger, query.WaitingReason = strings.TrimSpace(query.Purpose), strings.TrimSpace(query.Trigger), strings.TrimSpace(query.WaitingReason)
	query.TargetType, query.TargetID = strings.TrimSpace(query.TargetType), strings.TrimSpace(query.TargetID)
	query.InitiatorActorID = strings.TrimSpace(query.InitiatorActorID)
	if query.Limit < 1 || query.Limit > 500 || (query.TargetType == "") != (query.TargetID == "") ||
		(query.WaitingReason != "" && query.Status != model.WorkWaitingHuman) {
		return WorkPage{}, fmt.Errorf("work list requires limit in [1,500], complete target, and waiting_reason only with waiting_human status")
	}
	if query.Status != "" && !validWorkListStatus(query.Status) {
		return WorkPage{}, fmt.Errorf("unknown work status %q", query.Status)
	}
	if query.UpdatedFrom != nil && query.UpdatedBefore != nil && !query.UpdatedFrom.Before(*query.UpdatedBefore) {
		return WorkPage{}, fmt.Errorf("updated_from must be earlier than updated_before")
	}
	selectorHash := workListSelectorHash(query)
	var beforeTime time.Time
	var beforeID string
	if query.Cursor != "" {
		cursor, err := decodeCursor[workListCursor](query.Cursor)
		if err != nil || cursor.SelectorHash != selectorHash || cursor.WorkID == "" {
			return WorkPage{}, fmt.Errorf("%w: work selector", ErrInvalidCursor)
		}
		beforeTime, err = time.Parse(time.RFC3339Nano, cursor.UpdatedAt)
		if err != nil {
			return WorkPage{}, fmt.Errorf("%w: work time", ErrInvalidCursor)
		}
		beforeID = cursor.WorkID
	}

	index := "ix_recruiting_work_updated"
	var clauses []string
	var args []any
	if query.Cursor != "" {
		clauses = append(clauses, "(updated_at < ? OR (updated_at = ? AND work_id < ?))")
		args = append(args, beforeTime.UTC(), beforeTime.UTC(), beforeID)
	}
	if query.TargetID != "" {
		index = "ix_recruiting_work_target"
		clauses, args = append(clauses, "target_type = ?", "target_id = ?"), append(args, query.TargetType, query.TargetID)
	}
	if query.InitiatorActorID != "" {
		if query.TargetID == "" {
			index = "ix_recruiting_work_initiator"
		}
		clauses, args = append(clauses, "initiator_actor_id = ?"), append(args, query.InitiatorActorID)
	}
	if query.Status != "" {
		if query.TargetID == "" && query.InitiatorActorID == "" {
			index = "ix_recruiting_work_status_updated"
		}
		clauses, args = append(clauses, "status = ?"), append(args, query.Status)
	}
	if query.Purpose != "" {
		if query.TargetID == "" && query.InitiatorActorID == "" && query.Status == "" {
			index = "ix_recruiting_work_purpose_updated"
		}
		clauses, args = append(clauses, "purpose = ?"), append(args, query.Purpose)
	}
	if query.Trigger != "" {
		if query.TargetID == "" && query.InitiatorActorID == "" && query.Status == "" && query.Purpose == "" {
			index = "ix_recruiting_work_trigger_updated"
		}
		clauses, args = append(clauses, "trigger_kind = ?"), append(args, query.Trigger)
	}
	if query.WaitingReason != "" {
		clauses, args = append(clauses, "JSON_UNQUOTE(JSON_EXTRACT(state_json, '$.waiting_reason')) = ?"), append(args, query.WaitingReason)
	}
	if query.UpdatedFrom != nil {
		clauses, args = append(clauses, "updated_at >= ?"), append(args, query.UpdatedFrom.UTC())
	}
	if query.UpdatedBefore != nil {
		clauses, args = append(clauses, "updated_at < ?"), append(args, query.UpdatedBefore.UTC())
	}
	args = append(args, query.Limit+1)
	sqlText := fmt.Sprintf(`
SELECT state_json, business_key, priority, capability, origin, profile_id, not_before, deadline_at, updated_at, work_id
FROM recruiting_works FORCE INDEX (%s)
WHERE %s
ORDER BY updated_at DESC, work_id DESC
LIMIT ?`, index, workListWhere(clauses))
	rows, err := r.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return WorkPage{}, fmt.Errorf("list operational works: %w", err)
	}
	defer rows.Close()
	type rowValue struct {
		record  WorkRecord
		updated time.Time
		workID  string
	}
	values := make([]rowValue, 0, query.Limit+1)
	for rows.Next() {
		var value rowValue
		var state []byte
		var businessKey, capability, origin, profileID sql.NullString
		var deadline sql.NullTime
		if err := rows.Scan(&state, &businessKey, &value.record.Placement.Priority, &capability, &origin, &profileID,
			&value.record.Placement.NotBefore, &deadline, &value.updated, &value.workID); err != nil {
			return WorkPage{}, fmt.Errorf("scan operational work: %w", err)
		}
		if err := json.Unmarshal(state, &value.record.Work); err != nil {
			return WorkPage{}, fmt.Errorf("decode operational work: %w", err)
		}
		value.record.Placement.BusinessKey, value.record.Placement.Capability = businessKey.String, capability.String
		value.record.Placement.Origin, value.record.Placement.ProfileID = origin.String, profileID.String
		value.record.Placement.NotBefore = value.record.Placement.NotBefore.UTC()
		if deadline.Valid {
			deadlineAt := deadline.Time.UTC()
			value.record.Placement.DeadlineAt = &deadlineAt
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return WorkPage{}, fmt.Errorf("read operational work page: %w", err)
	}
	page := WorkPage{HasMore: len(values) > query.Limit}
	if page.HasMore {
		values = values[:query.Limit]
	}
	page.Items = make([]WorkRecord, 0, len(values))
	for _, value := range values {
		page.Items = append(page.Items, value.record)
	}
	if page.HasMore && len(values) != 0 {
		last := values[len(values)-1]
		page.NextCursor = encodeCursor(workListCursor{SelectorHash: selectorHash, UpdatedAt: last.updated.UTC().Format(time.RFC3339Nano), WorkID: last.workID})
	}
	return page, nil
}

func workListWhere(clauses []string) string {
	if len(clauses) == 0 {
		return "TRUE"
	}
	return strings.Join(clauses, " AND ")
}

func workListSelectorHash(query WorkListQuery) string {
	type selector struct {
		Status, Purpose, Trigger, WaitingReason, TargetType, TargetID, InitiatorActorID string
		UpdatedFrom, UpdatedBefore                                                      string
	}
	value := selector{Status: string(query.Status), Purpose: query.Purpose, Trigger: query.Trigger, WaitingReason: query.WaitingReason,
		TargetType: query.TargetType, TargetID: query.TargetID, InitiatorActorID: query.InitiatorActorID}
	if query.UpdatedFrom != nil {
		value.UpdatedFrom = query.UpdatedFrom.UTC().Format(time.RFC3339Nano)
	}
	if query.UpdatedBefore != nil {
		value.UpdatedBefore = query.UpdatedBefore.UTC().Format(time.RFC3339Nano)
	}
	content, _ := json.Marshal(value)
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func validWorkListStatus(status model.WorkStatus) bool {
	switch status {
	case model.WorkOpen, model.WorkRunning, model.WorkWaitingRetry, model.WorkWaitingHuman,
		model.WorkPaused, model.WorkCompleted, model.WorkFailed, model.WorkCanceled:
		return true
	default:
		return false
	}
}

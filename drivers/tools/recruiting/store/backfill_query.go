package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// BackfillOutputRecord keeps the potentially large result body out of list
// responses. Operators page metadata first and fetch one selected body by ID.
type BackfillOutputRecord struct {
	Output     model.BackfillOutput `json:"output"`
	OutputJSON json.RawMessage      `json:"output_json"`
}

type BackfillOutputPage struct {
	Outputs    []model.BackfillOutput `json:"outputs"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

type BackfillGapPage struct {
	Items      []model.BackfillItem `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

func (r *Repository) GetBackfillOutput(ctx context.Context, backfillID, outputID string) (BackfillOutputRecord, error) {
	backfillID, outputID = strings.TrimSpace(backfillID), strings.TrimSpace(outputID)
	if backfillID == "" || outputID == "" {
		return BackfillOutputRecord{}, fmt.Errorf("backfill and output are required")
	}
	var state, outputJSON []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json, output_json FROM recruiting_backfill_outputs
WHERE backfill_id = ? AND output_id = ?`, backfillID, outputID).Scan(&state, &outputJSON)
	if err != nil {
		return BackfillOutputRecord{}, err
	}
	var output model.BackfillOutput
	if err := json.Unmarshal(state, &output); err != nil {
		return BackfillOutputRecord{}, err
	}
	if len(outputJSON) == 0 || len(outputJSON) > backfillResultMaxJSONBytes || !json.Valid(outputJSON) {
		return BackfillOutputRecord{}, fmt.Errorf("stored backfill output body is invalid")
	}
	return BackfillOutputRecord{Output: output, OutputJSON: append(json.RawMessage(nil), outputJSON...)}, nil
}

func (r *Repository) ListBackfillOutputs(ctx context.Context, backfillID, afterItemID string,
	limit int) (BackfillOutputPage, error) {
	backfillID, afterItemID = strings.TrimSpace(backfillID), strings.TrimSpace(afterItemID)
	if backfillID == "" || limit < 1 || limit > 500 {
		return BackfillOutputPage{}, fmt.Errorf("backfill and limit in [1,500] are required")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_backfill_outputs
WHERE backfill_id = ? AND item_id > ? ORDER BY item_id LIMIT ?`, backfillID, afterItemID, limit+1)
	if err != nil {
		return BackfillOutputPage{}, err
	}
	defer rows.Close()
	page := BackfillOutputPage{Outputs: make([]model.BackfillOutput, 0, limit)}
	for rows.Next() {
		var state []byte
		var output model.BackfillOutput
		if err := rows.Scan(&state); err != nil {
			return BackfillOutputPage{}, err
		}
		if err := json.Unmarshal(state, &output); err != nil {
			return BackfillOutputPage{}, err
		}
		page.Outputs = append(page.Outputs, output)
	}
	if err := rows.Err(); err != nil {
		return BackfillOutputPage{}, err
	}
	if len(page.Outputs) > limit {
		page.NextCursor = page.Outputs[limit-1].ItemID
		page.Outputs = page.Outputs[:limit]
	}
	return page, nil
}

// ListBackfillGaps is the operator-facing exception report. Failed items stay
// actionable; accepted gaps remain visible as permanent audited exceptions.
func (r *Repository) ListBackfillGaps(ctx context.Context, backfillID, afterItemID string,
	limit int) (BackfillGapPage, error) {
	backfillID, afterItemID = strings.TrimSpace(backfillID), strings.TrimSpace(afterItemID)
	if backfillID == "" || limit < 1 || limit > 500 {
		return BackfillGapPage{}, fmt.Errorf("backfill and limit in [1,500] are required")
	}
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM recruiting_backfills WHERE backfill_id = ?`, backfillID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BackfillGapPage{}, err
		}
		return BackfillGapPage{}, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_backfill_items
WHERE backfill_id = ? AND item_status IN ('failed', 'accepted_gap') AND item_id > ?
ORDER BY item_id LIMIT ?`, backfillID, afterItemID, limit+1)
	if err != nil {
		return BackfillGapPage{}, err
	}
	defer rows.Close()
	page := BackfillGapPage{Items: make([]model.BackfillItem, 0, limit)}
	for rows.Next() {
		var state []byte
		var item model.BackfillItem
		if err := rows.Scan(&state); err != nil {
			return BackfillGapPage{}, err
		}
		if err := json.Unmarshal(state, &item); err != nil {
			return BackfillGapPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return BackfillGapPage{}, err
	}
	if len(page.Items) > limit {
		page.NextCursor = page.Items[limit-1].ItemID
		page.Items = page.Items[:limit]
	}
	return page, nil
}

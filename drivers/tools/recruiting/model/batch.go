package model

import (
	"fmt"
	"strings"
)

type BatchItemStatus string

const (
	BatchItemSucceeded    BatchItemStatus = "succeeded"
	BatchItemFailed       BatchItemStatus = "failed"
	BatchItemSkipped      BatchItemStatus = "skipped"
	BatchItemWaitingHuman BatchItemStatus = "waiting_human"
	BatchItemCanceled     BatchItemStatus = "canceled"
)

type BatchItemResult struct {
	ItemKey string          `json:"item_key"`
	Status  BatchItemStatus `json:"status"`
	Detail  string          `json:"detail,omitempty"`
}

type BatchOutcome struct {
	Total        int `json:"total"`
	Succeeded    int `json:"succeeded"`
	Failed       int `json:"failed"`
	Skipped      int `json:"skipped"`
	WaitingHuman int `json:"waiting_human"`
	Canceled     int `json:"canceled"`
}

// CancelBatchChildren models parent cancellation without making the parent a
// transaction boundary. Every non-terminal child is independently canceled,
// which increments its acceptance version and fences any in-flight result.
// Terminal children are retained unchanged.
func CancelBatchChildren(parentWorkID string, children []Work) ([]Work, error) {
	if strings.TrimSpace(parentWorkID) == "" {
		return nil, fmt.Errorf("parent work ID is required")
	}
	seen := make(map[string]struct{}, len(children))
	result := make([]Work, 0, len(children))
	for _, child := range children {
		if child.ParentWorkID != parentWorkID || child.WorkID == "" {
			return nil, fmt.Errorf("work %q is not a child of %q", child.WorkID, parentWorkID)
		}
		if _, duplicate := seen[child.WorkID]; duplicate {
			return nil, fmt.Errorf("duplicate child work %q", child.WorkID)
		}
		seen[child.WorkID] = struct{}{}
		if child.Terminal() {
			result = append(result, child)
			continue
		}
		canceled, err := child.Cancel(child.Version)
		if err != nil {
			return nil, err
		}
		result = append(result, canceled)
	}
	return result, nil
}

func AggregateBatch(results []BatchItemResult) (BatchOutcome, error) {
	seen := map[string]struct{}{}
	out := BatchOutcome{Total: len(results)}
	for _, item := range results {
		if item.ItemKey == "" {
			return BatchOutcome{}, fmt.Errorf("batch item key is required")
		}
		if _, duplicate := seen[item.ItemKey]; duplicate {
			return BatchOutcome{}, fmt.Errorf("duplicate batch item key %q", item.ItemKey)
		}
		seen[item.ItemKey] = struct{}{}
		switch item.Status {
		case BatchItemSucceeded:
			out.Succeeded++
		case BatchItemFailed:
			out.Failed++
		case BatchItemSkipped:
			out.Skipped++
		case BatchItemWaitingHuman:
			out.WaitingHuman++
		case BatchItemCanceled:
			out.Canceled++
		default:
			return BatchOutcome{}, fmt.Errorf("unknown batch item status %q", item.Status)
		}
	}
	return out, nil
}

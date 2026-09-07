package recruiting

import (
	"fmt"
	"strings"
)

// ContractVersion is the first recruiting application vocabulary. It is
// independent from Atoll's wire version and can evolve without changing core.
const ContractVersion = "recruiting.v1alpha1"

const (
	ErrorPayloadInvalid       = "payload_invalid"
	ErrorNotFound             = "not_found"
	ErrorVersionConflict      = "version_conflict"
	ErrorBusinessKeyConflict  = "business_key_conflict"
	ErrorCommandConflict      = "command_conflict"
	ErrorQualityRejected      = "quality_rejected"
	ErrorBudgetBlocked        = "budget_blocked"
	ErrorWaitingHuman         = "waiting_human"
	ErrorUnauthorizedExecutor = "unauthorized_executor"
	ErrorInternalUnavailable  = "internal_unavailable"
)

// PageRequest and PageInfo freeze cursor pagination at the product boundary.
// Cursors are opaque; callers must never parse or manufacture them.
type PageRequest struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type PageInfo struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

func (p PageRequest) Validate(max int) error {
	if p.Limit < 0 || p.Limit > max {
		return fmt.Errorf("limit must be in [0,%d]", max)
	}
	if strings.ContainsAny(p.Cursor, "\r\n") {
		return fmt.Errorf("cursor contains control characters")
	}
	return nil
}

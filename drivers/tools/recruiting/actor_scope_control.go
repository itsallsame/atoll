package recruiting

import (
	"context"
	"errors"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

type scopeControlReconcileResult struct {
	OperationID     string
	WorksScanned    int
	WorksPaused     int
	WorksCanceled   int
	AttemptsExpired int
	Completed       bool
	Conflict        bool
}

// reconcileScopeControl projects at most one operation and at most limit Work
// per tick, preserving a shared upper bound even when many scopes are paused.
func reconcileScopeControl(ctx context.Context, repository *store.Repository, limit int,
	now time.Time) (scopeControlReconcileResult, error) {
	operations, err := repository.ListScopeControlOperationsForReconcile(ctx, 1)
	if err != nil || len(operations) == 0 {
		return scopeControlReconcileResult{}, err
	}
	operation := operations[0]
	next, err := repository.ReconcileScopeControlOperation(ctx, operation.OperationID, operation.Version, limit, now)
	if err != nil {
		var conflict *model.VersionConflictError
		if errors.As(err, &conflict) || errors.Is(err, store.ErrProgressConflict) {
			return scopeControlReconcileResult{OperationID: operation.OperationID, Conflict: true}, nil
		}
		return scopeControlReconcileResult{}, err
	}
	return scopeControlReconcileResult{
		OperationID:     next.OperationID,
		WorksScanned:    int(next.WorksScanned - operation.WorksScanned),
		WorksPaused:     int(next.WorksPaused - operation.WorksPaused),
		WorksCanceled:   int(next.WorksCanceled - operation.WorksCanceled),
		AttemptsExpired: int(next.AttemptsExpired - operation.AttemptsExpired),
		Completed:       next.Status == model.ScopeControlCompleted,
	}, nil
}

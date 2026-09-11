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
	WorksResumed    int
	AttemptsExpired int
	RootsSettled    int
	SourcesScanned  int
	CatchUpsQueued  int
	CatchUpsSkipped int
	Dispatches      int
	Completed       bool
	Conflict        bool
}

// reconcileScopeControl projects at most one operation and at most limit Work
// per tick, preserving a shared upper bound even when many scopes are paused.
func reconcileScopeControl(ctx context.Context, repository *store.Repository, limit int,
	now time.Time, targets []store.ExecutionDispatchTarget) (scopeControlReconcileResult, error) {
	operations, err := repository.ListScopeControlOperationsForReconcile(ctx, 1)
	if err != nil {
		return scopeControlReconcileResult{}, err
	}
	if len(operations) == 0 {
		catchUps, catchUpErr := repository.ListScopeControlOperationsForCatchUp(ctx, 1)
		if catchUpErr != nil {
			return scopeControlReconcileResult{}, catchUpErr
		}
		if len(catchUps) != 0 {
			operation := catchUps[0]
			result := scopeControlReconcileResult{OperationID: operation.OperationID}
			for processed := 0; processed < limit; processed++ {
				step, stepErr := repository.ReconcileScopeControlCatchUpSource(ctx, operation.OperationID,
					operation.Version, now, targets)
				if stepErr != nil {
					var conflict *model.VersionConflictError
					if errors.As(stepErr, &conflict) || errors.Is(stepErr, store.ErrProgressConflict) {
						result.Conflict = true
						return result, nil
					}
					return scopeControlReconcileResult{}, stepErr
				}
				operation = step.Operation
				result.Dispatches += step.Dispatches
				if step.Occurrence == nil {
					result.Completed = operation.Status == model.ScopeControlCompleted
					break
				}
				result.SourcesScanned++
				if step.Occurrence.Disposition == model.ScopeCatchUpQueued {
					result.CatchUpsQueued++
				} else {
					result.CatchUpsSkipped++
				}
			}
			return result, nil
		}
		operations, err = repository.ListScopeControlOperationsForRootSettlement(ctx, 1)
		if err != nil || len(operations) == 0 {
			return scopeControlReconcileResult{}, err
		}
		operation := operations[0]
		next, settled, settleErr := repository.SettleCompletedScopeControlRoots(ctx, operation.OperationID,
			operation.Version, limit, now)
		if settleErr != nil {
			var conflict *model.VersionConflictError
			if errors.As(settleErr, &conflict) || errors.Is(settleErr, store.ErrProgressConflict) {
				return scopeControlReconcileResult{OperationID: operation.OperationID, Conflict: true}, nil
			}
			return scopeControlReconcileResult{}, settleErr
		}
		return scopeControlReconcileResult{OperationID: operation.OperationID, RootsSettled: settled,
			Completed: next.Status == model.ScopeControlCompleted}, nil
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
		WorksResumed:    int(next.WorksResumed - operation.WorksResumed),
		AttemptsExpired: int(next.AttemptsExpired - operation.AttemptsExpired),
		Completed:       next.Status == model.ScopeControlCompleted,
	}, nil
}

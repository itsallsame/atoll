package store

import (
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ExecutionFailurePolicy is application configuration, not executor input.
// The control plane therefore owns retry timing even when the executor reports
// whether the concrete failure looked transient.
type ExecutionFailurePolicy struct {
	Version              uint64
	MaxAutomaticAttempts uint64
	BaseDelay            time.Duration
	MaxDelay             time.Duration
	ThrottledDelay       time.Duration
}

func (p ExecutionFailurePolicy) Validate() error {
	if p.Version == 0 || p.MaxAutomaticAttempts < 1 || p.MaxAutomaticAttempts > 100 ||
		p.BaseDelay < time.Second || p.BaseDelay > 24*time.Hour || p.MaxDelay < p.BaseDelay || p.MaxDelay > 7*24*time.Hour ||
		p.ThrottledDelay < p.BaseDelay || p.ThrottledDelay > 7*24*time.Hour {
		return fmt.Errorf("invalid execution failure policy")
	}
	return nil
}

func (p ExecutionFailurePolicy) Decide(work model.Work, report executioncontract.FailureReport, at time.Time) (model.ExecutionFailureDecision, error) {
	if err := p.Validate(); err != nil {
		return model.ExecutionFailureDecision{}, err
	}
	if err := report.Validate(report.Artifact.AttemptID); err != nil || at.IsZero() {
		return model.ExecutionFailureDecision{}, fmt.Errorf("failure decision requires valid report and time")
	}
	count := work.AutomaticAttempts + 1
	decision := model.ExecutionFailureDecision{PolicyVersion: p.Version, AttemptCount: count, FailureClass: report.Class}
	if report.NeedsRepair || !report.Retryable || count >= p.MaxAutomaticAttempts {
		decision.Route = model.FailureHuman
		return decision, nil
	}
	delay := p.BaseDelay
	for step := uint64(1); step < count && delay < p.MaxDelay; step++ {
		if delay > p.MaxDelay/2 {
			delay = p.MaxDelay
			break
		}
		delay *= 2
	}
	if report.Class == "throttled" && delay < p.ThrottledDelay {
		delay = p.ThrottledDelay
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	decision.Route = model.FailureRetry
	decision.RetryNotBefore = at.Add(delay).UTC().Format(time.RFC3339Nano)
	return decision, nil
}

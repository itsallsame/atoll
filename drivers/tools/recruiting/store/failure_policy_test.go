package store

import (
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestExecutionFailurePolicyBoundsRetriesAndRoutesRepair(t *testing.T) {
	policy := testExecutionFailurePolicy()
	at := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	work, _ := model.NewWork("policy-work", "source", "source-1", "listing_sync", "timer")
	artifact, _ := model.NewArtifactMetadata("policy-artifact", model.ArtifactFailure, "sha256:failure", "artifact://failure",
		work.WorkID, "attempt-1", "operators", "30d", true)

	retry, err := policy.Decide(work, executioncontract.FailureReport{Class: "transport_timeout", Retryable: true, Artifact: artifact}, at)
	if err != nil || retry.Route != model.FailureRetry || retry.AttemptCount != 1 || retry.RetryNotBefore != at.Add(30*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("transient decision = %+v err=%v", retry, err)
	}
	throttled, err := policy.Decide(work, executioncontract.FailureReport{Class: "throttled", Retryable: true, Artifact: artifact}, at)
	if err != nil || throttled.Route != model.FailureRetry || throttled.RetryNotBefore != at.Add(5*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("throttled decision = %+v err=%v", throttled, err)
	}
	repair, err := policy.Decide(work, executioncontract.FailureReport{Class: "parse_error", NeedsRepair: true, Artifact: artifact}, at)
	if err != nil || repair.Route != model.FailureHuman || repair.RetryNotBefore != "" {
		t.Fatalf("repair decision = %+v err=%v", repair, err)
	}
	work.AutomaticAttempts = policy.MaxAutomaticAttempts - 1
	exhausted, err := policy.Decide(work, executioncontract.FailureReport{Class: "upstream_5xx", Retryable: true, Artifact: artifact}, at)
	if err != nil || exhausted.Route != model.FailureHuman || exhausted.AttemptCount != policy.MaxAutomaticAttempts {
		t.Fatalf("exhausted decision = %+v err=%v", exhausted, err)
	}
}

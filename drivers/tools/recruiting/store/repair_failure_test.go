package store

import (
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestControlPlaneDerivesRepairDomainFromFrozenExecution(t *testing.T) {
	at := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	baseWork, _ := model.NewWork("failed-work", "job", "job-1", "detail_sync", "timer")
	baseAttempt := model.Attempt{AttemptID: "attempt-1", WorkID: baseWork.WorkID,
		RecipeID: "detail-recipe", RecipeVersion: 4, RefreshGeneration: 8,
		ProfileID: "profile-1", ProfileVersion: 3}
	placement := WorkPlacement{Origin: "https://jobs.example.test", ProfileID: "profile-1", NotBefore: at}
	tests := []struct {
		name          string
		class         string
		purpose       string
		profile       bool
		wantDomain    model.FailureDomain
		wantDomainKey string
		wantVersion   string
	}{
		{"Recipe parser", "parse_error", "detail_sync", true, model.FailureRecipeVersion, "detail-recipe", "recipe:4"},
		{"single detail quality", "quality_rejected", "detail_sync", true, model.FailureSingleTarget, "job:job-1", "refresh:8"},
		{"Profile authentication", "captcha", "detail_sync", true, model.FailureProfile, "profile-1", "profile:3"},
		{"origin transport", "transport_timeout", "listing_sync", false, model.FailureOrigin, "https://jobs.example.test", "policy:5"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			work, attempt, candidatePlacement := baseWork, baseAttempt, placement
			work.Purpose = test.purpose
			if !test.profile {
				attempt.ProfileID, attempt.ProfileVersion, candidatePlacement.ProfileID = "", 0, ""
			}
			failure, err := newRepairFailure(work, attempt, candidatePlacement,
				executioncontract.FailureReport{Class: test.class, Signature: "stable.signature"}, 5, at)
			if err != nil || failure.Incident.Domain != test.wantDomain || failure.Incident.DomainKey != test.wantDomainKey ||
				failure.Incident.FailingVersion != test.wantVersion || failure.Incident.RepairWorkID != failure.Work.WorkID {
				t.Fatalf("repair failure = %+v err=%v", failure, err)
			}
		})
	}
}

func TestSharedRepairIdentityIgnoresAffectedWorkIdentity(t *testing.T) {
	at := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	placement := WorkPlacement{Origin: "https://shared.example.test", NotBefore: at}
	report := executioncontract.FailureReport{Class: "forbidden", Signature: "http.forbidden"}
	var first repairFailure
	for index, workID := range []string{"work-a", "work-b"} {
		work, _ := model.NewWork(workID, "source", "source-"+workID, "listing_sync", "timer")
		attempt := model.Attempt{AttemptID: "attempt-" + workID, WorkID: workID}
		failure, err := newRepairFailure(work, attempt, placement, report, 7, at)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = failure
			continue
		}
		if failure.Incident.RepairKey != first.Incident.RepairKey || failure.Work.WorkID != first.Work.WorkID {
			t.Fatalf("same shared failure diverged: first=%+v second=%+v", first, failure)
		}
	}
}

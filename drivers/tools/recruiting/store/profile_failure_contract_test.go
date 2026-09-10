package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestAuthenticationFailureAtomicallyFencesSharedProfile(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	const prefix = "profile-auth-fence"
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, prefix, 3)
	profile, _ := model.NewBrowserProfile("profile-auth-fence-profile", prefix+".example.com",
		"authorized-device", "secret://profiles/profile-auth-fence/v1")
	if err := repository.CreateProfile(ctx, profile, offerAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	workIDs := listingWorkIDsBySourcePrefix(t, ctx, db, prefix+"-daily", prefix+"-source-")
	if len(workIDs) != 3 {
		t.Fatalf("Profile fixture work IDs=%+v", workIDs)
	}
	for _, workID := range workIDs {
		if _, err := db.ExecContext(ctx, `UPDATE recruiting_works SET profile_id = ? WHERE work_id = ?`,
			profile.ProfileID, workID); err != nil {
			t.Fatal(err)
		}
	}
	firstRecord, err := repository.GetWorkRecord(ctx, workIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-auth-fence-attempt",
		ExecutorActorID: "tool:profile-auth-executor:1", ExecutorIncarnation: "profile-auth-boot",
		Capability: firstRecord.Placement.Capability, Origin: firstRecord.Placement.Origin,
		ProfileID: profile.ProfileID, OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Attempt.ProfileID != profile.ProfileID || offer.Attempt.ProfileVersion != profile.Version {
		t.Fatalf("Profile execution offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	secondOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-auth-fence-attempt-2",
		ExecutorActorID: "tool:profile-auth-executor:2", ExecutorIncarnation: "profile-auth-boot-2",
		Capability: firstRecord.Placement.Capability, Origin: firstRecord.Placement.Origin,
		ProfileID: profile.ProfileID, OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || secondOffer.Attempt.ProfileVersion != profile.Version {
		t.Fatalf("second Profile execution offer=%+v err=%v", secondOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, secondOffer.Attempt.AttemptID,
		secondOffer.Attempt.ExecutorActorID, secondOffer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, secondOffer.Attempt.AttemptID,
		secondOffer.Attempt.ExecutorActorID, secondOffer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	failureAt := offerAt.Add(time.Second)
	artifact := mustResultArtifact(t, "profile-auth-fence-artifact", model.ArtifactFailure,
		offer.Work.WorkID, offer.Attempt.AttemptID)
	report := executioncontract.FailureReport{Class: "auth_expired", Retryable: false,
		Signature: "browser.profile.expired", Artifact: artifact}
	command := ExecutionTransitionCommand{CommandID: "profile-auth-fence-command", Word: executioncontract.TypeFailed,
		RequestHash: "sha256:profile-auth-fence-command", CorrelationID: "profile-auth-fence-correlation",
		RequestedBy: offer.Attempt.ExecutorActorID, AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Action: "fail", Reason: report.Class,
		Failure: &report, FailurePolicy: testExecutionFailurePolicy()}
	result, err := repository.ApplyExecutionTransitionCommand(ctx, command, failureAt)
	if err != nil || result.Replayed {
		t.Fatalf("Profile authentication failure=%+v err=%v", result, err)
	}
	replay, err := repository.ApplyExecutionTransitionCommand(ctx, command, failureAt)
	if err != nil || !replay.Replayed {
		t.Fatalf("Profile authentication failure replay=%+v err=%v", replay, err)
	}
	secondArtifact := mustResultArtifact(t, "profile-auth-fence-artifact-2", model.ArtifactFailure,
		secondOffer.Work.WorkID, secondOffer.Attempt.AttemptID)
	secondReport := executioncontract.FailureReport{Class: "auth_expired", Retryable: false,
		Signature: "browser.profile.expired", Artifact: secondArtifact}
	secondCommand := ExecutionTransitionCommand{CommandID: "profile-auth-fence-command-2", Word: executioncontract.TypeFailed,
		RequestHash: "sha256:profile-auth-fence-command-2", CorrelationID: "profile-auth-fence-correlation-2",
		RequestedBy: secondOffer.Attempt.ExecutorActorID, AttemptID: secondOffer.Attempt.AttemptID,
		ExecutorIncarnation: secondOffer.Attempt.ExecutorIncarnation, Action: "fail", Reason: secondReport.Class,
		Failure: &secondReport, FailurePolicy: testExecutionFailurePolicy()}
	if secondResult, err := repository.ApplyExecutionTransitionCommand(ctx, secondCommand, failureAt); err != nil || secondResult.Replayed {
		t.Fatalf("concurrent Profile authentication failure=%+v err=%v", secondResult, err)
	}
	storedProfile, err := repository.GetProfile(ctx, profile.ProfileID)
	if err != nil || storedProfile.AuthStatus != model.ProfileRepairing ||
		storedProfile.Version != profile.Version+1 || storedProfile.SecretRef != profile.SecretRef ||
		storedProfile.DeviceID != profile.DeviceID {
		t.Fatalf("failed Profile fence=%+v err=%v", storedProfile, err)
	}
	work, _ := repository.GetWork(ctx, offer.Work.WorkID)
	if work.Status != model.WorkWaitingHuman || work.WaitingReason != report.Class || work.BlockedByRepairWorkID == "" {
		t.Fatalf("authentication failure Work=%+v", work)
	}
	var incidentID string
	if err := db.QueryRowContext(ctx, `SELECT incident_id FROM recruiting_repair_incidents
WHERE repair_work_id = ?`, work.BlockedByRepairWorkID).Scan(&incidentID); err != nil {
		t.Fatal(err)
	}
	incident, err := repository.GetRepairIncidentAggregate(ctx, incidentID)
	if err != nil || incident.Domain != model.FailureProfile || incident.DomainKey != profile.ProfileID ||
		incident.FailingVersion != "profile:1" {
		t.Fatalf("Profile RepairIncident=%+v err=%v", incident, err)
	}
	if _, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-auth-fence-forbidden",
		ExecutorActorID: "tool:profile-auth-executor:1", ExecutorIncarnation: "profile-auth-boot-2",
		Capability: firstRecord.Placement.Capability, Origin: firstRecord.Placement.Origin,
		ProfileID: profile.ProfileID, OfferedAt: failureAt.Add(time.Minute),
		BudgetPolicy: testExecutionBudgetPolicy()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repairing Profile allowed another Work offer: %v", err)
	}
	var profileEvents, receipts, affected int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE aggregate_type = 'profile' AND aggregate_id = ? AND event_kind = 'profile.repair_required'`,
		profile.ProfileID).Scan(&profileEvents)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id IN (?, ?)`,
		command.CommandID, secondCommand.CommandID).Scan(&receipts)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_repair_affected_works WHERE incident_id = ?`,
		incidentID).Scan(&affected)
	if profileEvents != 1 || receipts != 2 || affected != 2 {
		t.Fatalf("Profile fence audit events=%d receipts=%d affected=%d", profileEvents, receipts, affected)
	}
}

func TestBudgetFailureDoesNotChangeProfileAuthenticationState(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	const prefix = "profile-budget-failure"
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, prefix, 1)
	profile, _ := model.NewBrowserProfile("profile-budget-failure-profile", prefix+".example.com",
		"authorized-device", "secret://profiles/profile-budget-failure/v1")
	if err := repository.CreateProfile(ctx, profile, offerAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	workIDs := listingWorkIDsBySourcePrefix(t, ctx, db, prefix+"-daily", prefix+"-source-")
	if len(workIDs) != 1 {
		t.Fatalf("budget fixture work IDs=%+v", workIDs)
	}
	if _, err := db.ExecContext(ctx, `UPDATE recruiting_works SET profile_id = ? WHERE work_id = ?`,
		profile.ProfileID, workIDs[0]); err != nil {
		t.Fatal(err)
	}
	record, _ := repository.GetWorkRecord(ctx, workIDs[0])
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-budget-failure-attempt",
		ExecutorActorID: "tool:profile-budget-executor:1", ExecutorIncarnation: "profile-budget-boot",
		Capability: record.Placement.Capability, Origin: record.Placement.Origin, ProfileID: profile.ProfileID,
		OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	artifact := mustResultArtifact(t, "profile-budget-failure-artifact", model.ArtifactFailure,
		offer.Work.WorkID, offer.Attempt.AttemptID)
	report := executioncontract.FailureReport{Class: "budget_revoked", Retryable: false,
		Signature: "budget.profile.revoked", Artifact: artifact}
	command := ExecutionTransitionCommand{CommandID: "profile-budget-failure-command", Word: executioncontract.TypeFailed,
		RequestHash: "sha256:profile-budget-failure-command", CorrelationID: "profile-budget-failure-correlation",
		RequestedBy: offer.Attempt.ExecutorActorID, AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Action: "fail", Reason: report.Class,
		Failure: &report, FailurePolicy: testExecutionFailurePolicy()}
	if result, err := repository.ApplyExecutionTransitionCommand(ctx, command, offerAt.Add(time.Second)); err != nil || result.Replayed {
		t.Fatalf("Profile budget failure=%+v err=%v", result, err)
	}
	stored, err := repository.GetProfile(ctx, profile.ProfileID)
	if err != nil || stored != profile {
		t.Fatalf("budget failure changed Profile authentication state: got=%+v want=%+v err=%v", stored, profile, err)
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE aggregate_type = 'profile' AND aggregate_id = ?`, profile.ProfileID).Scan(&events); err != nil || events != 0 {
		t.Fatalf("budget failure emitted Profile authentication event count=%d err=%v", events, err)
	}
}

func listingWorkIDsBySourcePrefix(t *testing.T, ctx context.Context, db *sql.DB, dailyRunID, sourcePrefix string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT listing_work_id FROM recruiting_source_occurrences
WHERE daily_run_id = ? AND source_id LIKE ? ORDER BY source_id`, dailyRunID, sourcePrefix+"%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var workIDs []string
	for rows.Next() {
		var workID string
		if err := rows.Scan(&workID); err != nil {
			t.Fatal(err)
		}
		workIDs = append(workIDs, workID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return workIDs
}

package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourceProfileBindingIsAtomicVersionedAndSecretFree(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("profile-binding-company", "Profile Binding", "https://profile-binding.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("profile-binding-source", company.CompanyID,
		"https://profile-binding.example/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeBrowserRecipeForProfileBinding(t, "profile-binding-listing", model.RecipeListing,
		"profile-binding.example", 1, "profile-binding-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	assignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing, recipe.RecipeID,
		recipe.Version, recipe.ContractHash, now.Format(time.RFC3339))
	ready, _ := validating.PublishValidated(validating.Version, assignment,
		verifiedStoreAssessment(validating, assignment, now))
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
		t.Fatal(err)
	}
	secretRef := "secret://device/profile-binding-cookie-v1"
	profile, _ := model.NewBrowserProfile("profile-binding-profile", "profile-binding.example",
		"tool:profile-binding-device", secretRef)
	if err := repository.CreateProfile(ctx, profile, now); err != nil {
		t.Fatal(err)
	}

	nextSource, _ := ready.BindProfile(ready.Version, model.RecipeListing, profile.ProfileID)
	binding, _ := model.NewSourceProfileBinding(ready.SourceID, model.RecipeListing, profile.ProfileID, now.Add(time.Second))
	response, _ := json.Marshal(map[string]any{"source_id": ready.SourceID, "profile_id": profile.ProfileID,
		"source_version": nextSource.Version, "binding_version": binding.Version})
	receipt, _ := model.NewCommandReceipt("profile-binding-command", "recruiting.source.profile.bind",
		"sha256:profile-binding-request", response)
	audit, _ := json.Marshal(map[string]any{"requested_by": "human:operator", "profile_id": profile.ProfileID})
	event, _ := model.NewEventIntent("profile-binding-event", "source.profile_bound", "source_profile_binding",
		ready.SourceID+"|listing", binding.Version, now.Add(time.Second).Format(time.RFC3339Nano), receipt.CommandID, audit)
	result, err := repository.ApplySourceProfileBindingCommand(ctx, ready.Version, 0, nextSource, binding,
		receipt, event, now.Add(time.Second))
	if err != nil || result.Replayed {
		t.Fatalf("bind result=%+v err=%v", result, err)
	}
	replay, err := repository.ApplySourceProfileBindingCommand(ctx, ready.Version, 0, nextSource, binding,
		receipt, event, now.Add(time.Second))
	if err != nil || !replay.Replayed {
		t.Fatalf("binding replay=%+v err=%v", replay, err)
	}
	storedSource, _ := repository.GetSource(ctx, ready.SourceID)
	storedBinding, _ := repository.GetSourceProfileBinding(ctx, ready.SourceID, model.RecipeListing)
	if storedSource.ListingProfileID != profile.ProfileID || storedSource.Version != ready.Version+1 ||
		storedSource.ReadinessStatus != model.SourceRepairing || storedBinding != binding {
		t.Fatalf("stored source=%+v binding=%+v", storedSource, storedBinding)
	}
	var historyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_source_profile_binding_history
WHERE source_id = ? AND recipe_kind = 'listing'`, ready.SourceID).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("binding history=%d err=%v", historyCount, err)
	}
	var eventPayload []byte
	if err := db.QueryRowContext(ctx, `SELECT payload_json FROM recruiting_event_outbox WHERE event_id = ?`,
		event.EventID).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(response), secretRef) || strings.Contains(string(eventPayload), secretRef) {
		t.Fatal("Profile SecretRef escaped into command response or event")
	}

	preparation, err := repository.PrepareSourceValidation(ctx, storedSource.SourceID, recipe.RecipeID, recipe.Version)
	if err != nil {
		t.Fatal(err)
	}
	validatingAgain, validationRun, _ := preparation.NewRun("profile-binding-validation-run",
		"profile-binding-validation-work", assignment.AssignmentVersion, now.Add(2*time.Second).Format(time.RFC3339Nano))
	validationWork, _ := model.NewWork(validationRun.WorkID, "source", storedSource.SourceID, "source_validation", "manual")
	validationPlacement := WorkPlacement{BusinessKey: "source-validation|" + validationRun.ListingRunID,
		Priority: 300, Capability: "browser.recipe", Origin: validationRun.ListingExecution.Origin,
		ProfileID: profile.ProfileID, NotBefore: now.Add(2 * time.Second)}
	validationReceipt, _ := model.NewCommandReceipt("profile-binding-validation-command", "recruiting.source.validate",
		"sha256:profile-binding-validation", json.RawMessage(`{"validating":true}`))
	validationEvent, _ := model.NewEventIntent("profile-binding-validation-event", "source.validation_started", "source",
		storedSource.SourceID, validatingAgain.Version, now.Add(2*time.Second).Format(time.RFC3339Nano),
		validationReceipt.CommandID, json.RawMessage(`{}`))
	validationDispatch, _ := NewExecutionDispatchIntent("profile-binding-validation-dispatch", "tool:wrong-fleet-device",
		"browser.recipe", validationPlacement.Origin, profile.ProfileID, "source_validation", validationReceipt.CommandID,
		validationPlacement.NotBefore)
	if _, err := repository.ApplySourceValidationCommand(ctx, storedSource.Version, assignment.AssignmentVersion,
		validatingAgain, validationRun, validationWork, validationPlacement, validationReceipt, validationEvent,
		&validationDispatch, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var validationTarget string
	if err := db.QueryRowContext(ctx, `SELECT target_actor_id FROM recruiting_execution_dispatch_outbox
WHERE dispatch_id = ?`, validationDispatch.DispatchID).Scan(&validationTarget); err != nil || validationTarget != profile.DeviceID {
		t.Fatalf("persistent Profile validation target=%q err=%v", validationTarget, err)
	}
	revalidated, _ := validatingAgain.PublishValidated(validatingAgain.Version, assignment,
		verifiedStoreAssessment(validatingAgain, assignment, now.Add(2*time.Second)))
	if err := repository.UpdateSourceCAS(ctx, validatingAgain.Version, revalidated, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	canceledValidation, _ := validationWork.Cancel(validationWork.Version)
	if err := repository.UpdateWorkCAS(ctx, validationWork.Version, canceledValidation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	discovering, _ := company.StartDiscovery(company.Version)
	initializing, _ := discovering.StartInitialization(discovering.Version)
	readyCompany, _ := initializing.MarkReady(initializing.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, discovering, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateCompanyCAS(ctx, discovering.Version, initializing, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateCompanyCAS(ctx, initializing.Version, readyCompany, now); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2092, 1, 2, 0, 0, 0, 0, time.UTC)
	plan, err := repository.PlanDailyRunAtCutoff(ctx, DailyPlanRequest{DailyRunID: "profile-binding-daily",
		ScheduleDate: "2092-01-02", Schedule: model.DailySchedule{PolicyVersion: 1,
			CutoffAt: cutoff.Format(time.RFC3339), WindowStartAt: cutoff.Add(time.Hour).Format(time.RFC3339),
			WindowEndAt: cutoff.Add(3 * time.Hour).Format(time.RFC3339)},
		TriggerID: "profile-binding-timer", EventID: "profile-binding-daily-event"}, cutoff)
	if err != nil || plan.Occurrences < 1 {
		t.Fatalf("daily profile plan=%+v err=%v", plan, err)
	}
	materialized, err := repository.MaterializeDueOccurrenceWorksWithDispatch(ctx, cutoff.Add(3*time.Hour), 500,
		"tool:recruiting", "profile-binding-due", cutoff.Add(2*time.Hour),
		[]ExecutionDispatchTarget{{ActorID: "tool:wrong-fleet-device", Capability: "browser.recipe"}})
	if err != nil || materialized.Queued < 1 || materialized.DispatchesQueued < 1 {
		t.Fatalf("daily Profile materialization=%+v err=%v", materialized, err)
	}
	var occurrenceState, workProfile, dispatchTarget, dispatchProfile []byte
	if err := db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_occurrences
WHERE daily_run_id = ? AND source_id = ?`, plan.Run.DailyRunID, ready.SourceID).Scan(&occurrenceState); err != nil {
		t.Fatal(err)
	}
	var occurrence model.SourceOccurrence
	if err := json.Unmarshal(occurrenceState, &occurrence); err != nil || occurrence.ProfileID != profile.ProfileID {
		t.Fatalf("frozen occurrence=%+v err=%v", occurrence, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(profile_id, '') FROM recruiting_works WHERE work_id = ?`,
		occurrence.WorkID).Scan(&workProfile); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT target_actor_id, COALESCE(profile_id, '')
FROM recruiting_execution_dispatch_outbox WHERE cause_id = ? AND profile_id = ?`,
		"profile-binding-due", profile.ProfileID).Scan(&dispatchTarget, &dispatchProfile); err != nil {
		t.Fatal(err)
	}
	if string(workProfile) != profile.ProfileID || string(dispatchTarget) != profile.DeviceID || string(dispatchProfile) != profile.ProfileID {
		t.Fatalf("Profile routing work=%q target=%q dispatch_profile=%q", workProfile, dispatchTarget, dispatchProfile)
	}
	record, err := repository.GetWorkRecord(ctx, occurrence.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	offerAt := record.Placement.NotBefore
	wrongAttemptID := "profile-binding-wrong-device-attempt"
	if _, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: wrongAttemptID,
		ExecutorActorID: "tool:wrong-fleet-device:1", ExecutorIncarnation: "wrong-device-boot",
		Capability: record.Placement.Capability, Origin: record.Placement.Origin, ProfileID: profile.ProfileID,
		OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy()}); err == nil ||
		!strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("wrong device claimed Profile work: %v", err)
	}
	var wrongAttempts int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts WHERE attempt_id = ?`,
		wrongAttemptID).Scan(&wrongAttempts); err != nil || wrongAttempts != 0 {
		t.Fatalf("wrong device left Attempt count=%d err=%v", wrongAttempts, err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-binding-authorized-attempt",
		ExecutorActorID: profile.DeviceID + ":1", ExecutorIncarnation: "authorized-device-boot",
		Capability: record.Placement.Capability, Origin: record.Placement.Origin, ProfileID: profile.ProfileID,
		OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Work.WorkID != occurrence.WorkID || offer.Attempt.ProfileID != profile.ProfileID ||
		offer.Attempt.ProfileVersion != profile.Version {
		t.Fatalf("authorized Profile offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FailListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, "test_cleanup", offerAt); err != nil {
		t.Fatal(err)
	}
	pauseExecutionSource(t, ctx, repository, ready.SourceID, offerAt)
}

func TestListingPageRoutesDerivedDetailWorkToItsIndependentProfileDevice(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2093, 2, 3, 0, 0, 0, 0, time.UTC)
	const prefix = "profile-detail-routing"
	company := persistExecutionReadyCompany(t, ctx, repository, prefix, now)
	source := persistExecutionReadySource(t, ctx, repository, company, prefix, prefix+"-source", now)

	oldDetail := *source.DetailAssignment
	browserDetail := activeBrowserRecipeForProfileBinding(t, prefix+"-detail-browser", model.RecipeDetail,
		prefix+".example.com", 1, oldDetail.ContractHash)
	if err := repository.CreateRecipe(ctx, browserDetail, now); err != nil {
		t.Fatal(err)
	}
	replacement, _ := oldDetail.Replace(oldDetail.AssignmentVersion, browserDetail.RecipeID,
		browserDetail.Version, browserDetail.ContractHash, now.Add(time.Second).Format(time.RFC3339Nano))
	withBrowserDetail, _ := source.AssignRecipe(source.Version, replacement, false)
	if err := repository.PublishSourceAssignment(ctx, source.Version, oldDetail.AssignmentVersion,
		withBrowserDetail, replacement, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	profile, _ := model.NewBrowserProfile(prefix+"-profile", prefix+".example.com",
		"tool:"+prefix+"-device", "secret://profiles/"+prefix+"/v1")
	if err := repository.CreateProfile(ctx, profile, now); err != nil {
		t.Fatal(err)
	}
	boundSource, _ := withBrowserDetail.BindProfile(withBrowserDetail.Version, model.RecipeDetail, profile.ProfileID)
	binding, _ := model.NewSourceProfileBinding(source.SourceID, model.RecipeDetail, profile.ProfileID, now.Add(2*time.Second))
	receipt, _ := model.NewCommandReceipt(prefix+"-bind", "recruiting.source.profile.bind",
		"sha256:"+prefix+"-bind", json.RawMessage(`{"bound":true}`))
	event, _ := model.NewEventIntent(prefix+"-bind-event", "source.profile_bound", "source_profile_binding",
		source.SourceID+"|detail", binding.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), receipt.CommandID,
		json.RawMessage(`{"recipe_kind":"detail"}`))
	if _, err := repository.ApplySourceProfileBindingCommand(ctx, withBrowserDetail.Version, 0, boundSource,
		binding, receipt, event, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	cutoff := now.Add(3 * time.Second)
	plan, err := repository.PlanDailyRunAtCutoff(ctx, DailyPlanRequest{DailyRunID: prefix + "-daily",
		ScheduleDate: "2093-02-03", Schedule: model.DailySchedule{PolicyVersion: 1,
			CutoffAt: cutoff.Format(time.RFC3339Nano), WindowStartAt: cutoff.Add(time.Minute).Format(time.RFC3339Nano),
			WindowEndAt: cutoff.Add(time.Hour).Format(time.RFC3339Nano)}, TriggerID: prefix + "-timer",
		EventID: prefix + "-daily-event"}, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	var occurrenceState []byte
	if err := db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_occurrences
WHERE daily_run_id = ? AND source_id = ?`, plan.Run.DailyRunID, source.SourceID).Scan(&occurrenceState); err != nil {
		t.Fatal(err)
	}
	var occurrence model.SourceOccurrence
	if err := json.Unmarshal(occurrenceState, &occurrence); err != nil {
		t.Fatal(err)
	}
	dueAt, _ := time.Parse(time.RFC3339, occurrence.DueAt)
	materializeAt := dueAt.Add(time.Microsecond)
	if _, err := repository.MaterializeDueOccurrenceWorksWithDispatch(ctx, materializeAt, 500, "tool:recruiting",
		prefix+"-due", materializeAt, []ExecutionDispatchTarget{{ActorID: "tool:" + prefix + "-listing",
			Capability: "http.fetch"}}); err != nil {
		t.Fatal(err)
	}
	storedOccurrence, _ := repository.GetOccurrence(ctx, occurrence.OccurrenceID)
	record, err := repository.GetWorkRecord(ctx, storedOccurrence.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-listing-attempt",
		ExecutorActorID: "tool:" + prefix + "-listing:1", ExecutorIncarnation: prefix + "-listing-boot",
		Capability: record.Placement.Capability, Origin: record.Placement.Origin, OfferedAt: record.Placement.NotBefore,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, record.Placement.NotBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, record.Placement.NotBefore); err != nil {
		t.Fatal(err)
	}
	pageArtifact := mustResultArtifact(t, prefix+"-page", model.ArtifactPage, offer.Work.WorkID, offer.Attempt.AttemptID)
	page, err := repository.AcceptListingPage(ctx, ListingPageResult{CommandID: prefix + "-page-command",
		RequestHash: "sha256:" + prefix + "-page", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		PageSequence: 1, Terminal: true, Artifact: pageArtifact, ObservedAt: record.Placement.NotBefore,
		Observations: []model.ListingObservation{{ObservationID: prefix + "-observation",
			OccurrenceID: offer.Occurrence.OccurrenceID, SourceID: source.SourceID, SourceJobKey: prefix + "-job-key",
			DetailURL: "https://" + prefix + ".example.com/jobs/1", ActivityAt: now.Format(time.RFC3339),
			ListingFingerprint: "sha256:" + prefix + "-listing", RecipeID: offer.Attempt.RecipeID,
			RecipeVersion: offer.Attempt.RecipeVersion, ArtifactID: pageArtifact.ArtifactID}}})
	if err != nil || len(page.Items) != 1 || page.Items[0].DetailWork == nil {
		t.Fatalf("Profile detail page=%+v err=%v", page, err)
	}
	detailRecord, err := repository.GetWorkRecord(ctx, page.Items[0].DetailWork.WorkID)
	if err != nil || detailRecord.Placement.ProfileID != profile.ProfileID ||
		detailRecord.Placement.Capability != "browser.recipe" {
		t.Fatalf("Profile detail placement=%+v err=%v", detailRecord, err)
	}
	var target string
	if err := db.QueryRowContext(ctx, `SELECT target_actor_id FROM recruiting_execution_dispatch_outbox
WHERE profile_id = ? AND cause_kind = 'profile_work_materialized'`, profile.ProfileID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if target != profile.DeviceID {
		t.Fatalf("detail Profile dispatch target=%q want=%q", target, profile.DeviceID)
	}
	if _, err := repository.FailListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, "test_cleanup", record.Placement.NotBefore); err != nil {
		t.Fatal(err)
	}
	pauseExecutionSource(t, ctx, repository, source.SourceID, record.Placement.NotBefore)
}

func activeBrowserRecipeForProfileBinding(t *testing.T, id string, kind model.RecipeKind, scope string,
	version uint64, contract string) model.Recipe {
	t.Helper()
	recipe, err := model.NewRecipe(id, kind, scope, version, "content-"+id, contract, model.RecipeExecution{
		ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://" + id,
		RequiredCapability: "browser.recipe", Transport: model.RecipeTransportBrowser,
	})
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, err = recipe.Publish(recipe.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	return recipe
}

package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestListingObservationCreatesDetailOnlyForNewOrChangedJob(t *testing.T) {
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
	now := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("listing-company", "Listing", "https://listing.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("listing-source", company.CompanyID, "https://listing.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	observation := model.ListingObservation{
		ObservationID: "listing-observation-1", OccurrenceID: "listing-occurrence-1",
		SourceID: source.SourceID, SourceJobKey: "external-job-1", DetailURL: "https://listing.example.com/jobs/1",
		ActivityAt: "2026-09-08T00:59:00Z", ListingFingerprint: "fingerprint-1",
		RecipeID: "listing-recipe", RecipeVersion: 1, ArtifactID: "listing-artifact-1",
	}
	input := ListingIngest{
		Observation: observation, ObservedAt: now, NewJobID: "listing-job-1", DetailWorkID: "listing-work-1",
		Origin: "listing.example.com", Capability: "http.fetch", Priority: 10, NotBefore: now,
	}
	first, err := repository.ApplyListingObservation(ctx, input)
	if err != nil || first.DetailWork == nil || first.Job.RefreshGeneration != 1 {
		t.Fatalf("new listing = %+v %v", first, err)
	}
	replay, err := repository.ApplyListingObservation(ctx, input)
	if err != nil || !replay.ObservationReplayed || replay.DetailWork != nil {
		t.Fatalf("observation replay = %+v %v", replay, err)
	}
	conflictingReplay := input
	conflictingReplay.Observation.DetailURL = "https://listing.example.com/jobs/different"
	if _, err := repository.ApplyListingObservation(ctx, conflictingReplay); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("observation ID accepted different facts: %v", err)
	}
	overlap := input
	overlap.Observation.ObservationID = "listing-observation-2"
	overlap.Observation.OccurrenceID = "listing-occurrence-2"
	overlap.Observation.ArtifactID = "listing-artifact-2"
	overlap.DetailWorkID = "listing-work-overlap"
	unchanged, err := repository.ApplyListingObservation(ctx, overlap)
	if err != nil || unchanged.DetailWork != nil || unchanged.Job.Version != 1 || unchanged.Job.RefreshGeneration != 1 {
		t.Fatalf("safe overlap generated detail work: %+v %v", unchanged, err)
	}
	forced := overlap
	forced.Observation.ObservationID = "listing-observation-baseline-refresh"
	forced.Observation.OccurrenceID = "listing-baseline-2"
	forced.Observation.ArtifactID = "listing-artifact-baseline-refresh"
	forced.DetailWorkID = "listing-work-baseline-refresh"
	forced.ForceDetailRefresh = true
	refreshed, err := repository.ApplyListingObservation(ctx, forced)
	if err != nil || refreshed.DetailWork == nil || refreshed.Job.Version != 2 || refreshed.Job.RefreshGeneration != 2 {
		t.Fatalf("explicit baseline refresh did not generate detail: %+v %v", refreshed, err)
	}
	updated := overlap
	updated.Observation.ObservationID = "listing-observation-3"
	updated.Observation.ActivityAt = "2026-09-08T01:01:00Z"
	updated.Observation.ListingFingerprint = "fingerprint-2"
	updated.DetailWorkID = "listing-work-2"
	changed, err := repository.ApplyListingObservation(ctx, updated)
	if err != nil || changed.DetailWork == nil || changed.Job.RefreshGeneration != 3 || changed.Job.Version != 3 {
		t.Fatalf("changed listing did not generate detail: %+v %v", changed, err)
	}
	var observations, works int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?", source.SourceID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_works WHERE target_id = ?", changed.Job.JobID).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if observations != 4 || works != 3 {
		t.Fatalf("observations=%d works=%d", observations, works)
	}
	broken := updated
	broken.Observation.ObservationID = "listing-observation-rollback"
	broken.Observation.ActivityAt = "2026-09-08T01:02:00Z"
	broken.Observation.ListingFingerprint = "fingerprint-3"
	broken.DetailWorkID = "listing-work-1"
	if _, err := repository.ApplyListingObservation(ctx, broken); err == nil {
		t.Fatal("duplicate detail work ID unexpectedly committed listing transaction")
	}
	rolledBack, err := repository.GetJob(ctx, changed.Job.JobID)
	if err != nil || rolledBack.Version != 3 || rolledBack.RefreshGeneration != 3 {
		t.Fatalf("failed detail work left updated job: %+v %v", rolledBack, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?", source.SourceID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 4 {
		t.Fatalf("failed detail work left observation behind: %d", observations)
	}
}

func TestConcurrentEquivalentListingUpdatesConvergeToOneDetailWork(t *testing.T) {
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
	now := time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("listing-race-company", "Listing Race", "https://listing-race.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("listing-race-source", company.CompanyID, "https://listing-race.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	base := ListingIngest{
		Observation: model.ListingObservation{
			ObservationID: "race-observation-base", OccurrenceID: "race-occurrence-base", SourceID: source.SourceID,
			SourceJobKey: "race-external-job", DetailURL: "https://listing-race.example.com/jobs/1",
			ActivityAt: "2026-09-08T01:59:00Z", ListingFingerprint: "race-fingerprint-1",
			RecipeID: "listing-recipe", RecipeVersion: 1, ArtifactID: "race-artifact-base",
		},
		ObservedAt: now, NewJobID: "race-job", DetailWorkID: "race-work-base",
		Origin: "listing-race.example.com", Capability: "http.fetch", Priority: 10, NotBefore: now,
	}
	if _, err := repository.ApplyListingObservation(ctx, base); err != nil {
		t.Fatal(err)
	}
	inputs := []ListingIngest{base, base}
	for index := range inputs {
		inputs[index].Observation.ObservationID = "race-observation-update-" + string(rune('a'+index))
		inputs[index].Observation.OccurrenceID = "race-occurrence-update"
		inputs[index].Observation.ActivityAt = "2026-09-08T02:01:00Z"
		inputs[index].Observation.ListingFingerprint = "race-fingerprint-2"
		inputs[index].Observation.ArtifactID = "race-artifact-update-" + string(rune('a'+index))
		inputs[index].DetailWorkID = "race-work-update-" + string(rune('a'+index))
	}
	results := make(chan ListingIngestResult, 2)
	failures := make(chan error, 2)
	var group sync.WaitGroup
	for _, input := range inputs {
		input := input
		group.Add(1)
		go func() {
			defer group.Done()
			result, ingestErr := repository.ApplyListingObservation(ctx, input)
			results <- result
			failures <- ingestErr
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for ingestErr := range failures {
		if ingestErr != nil {
			t.Fatalf("concurrent listing update: %v", ingestErr)
		}
	}
	var detailCreated int
	for result := range results {
		if result.DetailWork != nil {
			detailCreated++
		}
	}
	if detailCreated != 1 {
		t.Fatalf("equivalent updates created %d detail works", detailCreated)
	}
	job, err := repository.GetJob(ctx, "race-job")
	if err != nil || job.RefreshGeneration != 2 || job.Version != 2 {
		t.Fatalf("converged job = %+v %v", job, err)
	}
	var works int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_works WHERE target_id = ?", job.JobID).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if works != 2 {
		t.Fatalf("new + one changed job should have 2 works, got %d", works)
	}
}

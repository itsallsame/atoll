package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestListingPageCommitIsAtomicReplayableAndWorkFenced(t *testing.T) {
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
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("page-company", "Page", "https://page.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("page-source", company.CompanyID, "https://page.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork("page-parent-work", "source", source.SourceID, "daily_listing", "schedule")
	if err := repository.CreateWork(ctx, work, WorkPlacement{BusinessKey: "page-parent-key", Capability: "http.fetch", Origin: "page.example.com", NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	running, _ := work.Start(work.Version)
	if err := repository.UpdateWorkCAS(ctx, work.Version, running, now); err != nil {
		t.Fatal(err)
	}

	progress1, _ := model.AdvanceListingPageProgress(nil, running, "cursor-2", "page-artifact-1", 1, false)
	page1 := ListingPageCommit{Progress: progress1, Items: []ListingIngest{pageListingInput(source.SourceID, "page-1", "page-artifact-1", now)}}
	result, err := repository.ApplyListingPage(ctx, page1, now)
	if err != nil || result.Replayed || len(result.Items) != 1 || result.Items[0].DetailWork == nil {
		t.Fatalf("first page = %+v %v", result, err)
	}
	replay, err := repository.ApplyListingPage(ctx, page1, now.Add(time.Second))
	if err != nil || !replay.Replayed {
		t.Fatalf("page acknowledgement replay = %+v %v", replay, err)
	}
	var observations, detailWorks int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?", source.SourceID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'detail_sync' AND origin = 'page.example.com'").Scan(&detailWorks); err != nil {
		t.Fatal(err)
	}
	if observations != 1 || detailWorks != 1 {
		t.Fatalf("page replay observations=%d detail_works=%d", observations, detailWorks)
	}

	paused, _ := running.Pause(running.Version)
	if err := repository.UpdateWorkCAS(ctx, running.Version, paused, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	staleProgress, _ := model.AdvanceListingPageProgress(&progress1, running, "cursor-3", "page-artifact-2", 1, false)
	stalePage := ListingPageCommit{Progress: staleProgress, Items: []ListingIngest{pageListingInput(source.SourceID, "page-2", "page-artifact-2", now)}}
	if _, err := repository.ApplyListingPage(ctx, stalePage, now); !errors.Is(err, ErrProgressConflict) {
		t.Fatalf("paused work accepted stale page = %v", err)
	}
	resumed, _ := paused.Resume(paused.Version)
	startedAgain, _ := resumed.Start(resumed.Version)
	if err := repository.UpdateWorkCAS(ctx, paused.Version, resumed, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateWorkCAS(ctx, resumed.Version, startedAgain, now); err != nil {
		t.Fatal(err)
	}

	progress2, _ := model.AdvanceListingPageProgress(&progress1, startedAgain, "", "page-artifact-2", 1, true)
	brokenInput := pageListingInput(source.SourceID, "page-2", "page-artifact-2", now)
	brokenInput.DetailWorkID = result.Items[0].DetailWork.WorkID
	if _, err := repository.ApplyListingPage(ctx, ListingPageCommit{Progress: progress2, Items: []ListingIngest{brokenInput}}, now); err == nil {
		t.Fatal("broken detail work did not roll back whole page")
	}
	latest, err := repository.GetLatestListingProgress(ctx, work.WorkID)
	if err != nil || latest.PageSequence != 1 {
		t.Fatalf("failed page advanced progress: %+v %v", latest, err)
	}
	if _, err := repository.GetJob(ctx, "page-job-page-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed page left job: %v", err)
	}
	page2 := ListingPageCommit{Progress: progress2, Items: []ListingIngest{pageListingInput(source.SourceID, "page-2", "page-artifact-2", now)}}
	if _, err := repository.ApplyListingPage(ctx, page2, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	latest, err = repository.GetLatestListingProgress(ctx, work.WorkID)
	if err != nil || latest.PageSequence != 2 || !latest.EndOfInput || latest.WorkAcceptanceVersion != startedAgain.AcceptanceVersion {
		t.Fatalf("resumed terminal progress: %+v %v", latest, err)
	}
}

func pageListingInput(sourceID, suffix, artifactID string, now time.Time) ListingIngest {
	return ListingIngest{
		Observation: model.ListingObservation{
			ObservationID: "page-observation-" + suffix, OccurrenceID: "page-occurrence", SourceID: sourceID,
			SourceJobKey: "page-external-" + suffix, DetailURL: "https://page.example.com/jobs/" + suffix,
			ActivityAt: now.Format(time.RFC3339), ListingFingerprint: "fingerprint-" + suffix,
			RecipeID: "page-listing-recipe", RecipeVersion: 1, ArtifactID: artifactID,
		},
		ObservedAt: now, NewJobID: "page-job-" + suffix, DetailWorkID: "page-detail-work-" + suffix,
		Origin: "page.example.com", Capability: "http.fetch", Priority: 10, NotBefore: now,
	}
}

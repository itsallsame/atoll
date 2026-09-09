package model

import "testing"

func TestBaselineListingAndDetailsHaveSeparateCompletion(t *testing.T) {
	baseline, err := NewBaselineGeneration("source-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err = baseline.FinalizeListing(baseline.Version, 10000)
	if err != nil || baseline.Status != BaselineDetailsPending || !baseline.ListingFinalized {
		t.Fatalf("listing finalize = %+v %v", baseline, err)
	}
	baseline, err = baseline.AccountDetails(baseline.Version, 9000, 500, 500)
	if err != nil || baseline.Status != BaselineWithExceptions || baseline.DetailsAccounted != 10000 {
		t.Fatalf("detail accounting = %+v %v", baseline, err)
	}
	if _, err := baseline.AccountDetails(baseline.Version, 1, 0, 0); err == nil {
		t.Fatal("completed baseline accepted more details")
	}
}

func TestBaselineMaterializationUsesVersionedBoundedPages(t *testing.T) {
	baseline, _ := NewBaselineGeneration("source-1", 1)
	baseline, _ = baseline.FinalizeListing(baseline.Version, 501)

	first, err := baseline.AdvanceMaterialization(baseline.Version, "job-0500", 500, false)
	if err != nil || first.MaterializationCursor != "job-0500" || first.MaterializedCount != 500 || first.MaterializationCompleted {
		t.Fatalf("first materialization page = %+v %v", first, err)
	}
	if _, err := first.AdvanceMaterialization(baseline.Version, "job-0501", 1, true); err == nil {
		t.Fatal("stale materialization version was accepted")
	}
	completed, err := first.AdvanceMaterialization(first.Version, "job-0501", 1, true)
	if err != nil || !completed.MaterializationCompleted || completed.MaterializedCount != 501 {
		t.Fatalf("final materialization page = %+v %v", completed, err)
	}
	if _, err := completed.AdvanceMaterialization(completed.Version, "job-0502", 1, true); err == nil {
		t.Fatal("completed materialization accepted another page")
	}
}

func TestEmptyBaselineCompletesMaterializationWithListing(t *testing.T) {
	baseline, _ := NewBaselineGeneration("source-1", 1)
	baseline, err := baseline.FinalizeListing(baseline.Version, 0)
	if err != nil || baseline.Status != BaselineCompleted || !baseline.MaterializationCompleted {
		t.Fatalf("empty baseline = %+v %v", baseline, err)
	}
}

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

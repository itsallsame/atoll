package recipeexec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func scanPage(next string, rows ...map[string]json.RawMessage) DocumentResult {
	var nextRaw json.RawMessage
	if next != "" {
		nextRaw, _ = json.Marshal(next)
	}
	return DocumentResult{Items: rows, Next: nextRaw, Quality: recipeabi.QualityProof{
		IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true, ItemCount: len(rows),
	}}
}

func scanItem(key, activity, title string) map[string]json.RawMessage {
	item := map[string]json.RawMessage{}
	item["job_key"], _ = json.Marshal(key)
	item["activity_at"], _ = json.Marshal(activity)
	item["title"], _ = json.Marshal(title)
	item["pinned"] = json.RawMessage("false")
	return item
}

func scanSpec() recipeabi.Spec {
	spec := listingSpec()
	spec.Listing.OverlapPages = 2
	spec.Listing.MaxPages = 10
	spec.Listing.FrontierWidth = 3
	return spec
}

func TestActivityScanCompletesEqualTimeGroupAndSubsequentOverlap(t *testing.T) {
	checkpoint := &recipeabi.CheckpointRef{Version: 7, LastActivityAt: "2026-09-08T10:00:00Z", FrontierKeys: []string{"old-a"}}
	scan, err := NewListingScan(scanSpec(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	pages := []DocumentResult{
		scanPage("p2", scanItem("new-2", "2026-09-08T12:00:00Z", "new 2"), scanItem("new-1", "2026-09-08T11:00:00Z", "new 1")),
		scanPage("p3", scanItem("same-a", "2026-09-08T10:00:00Z", "same a")),
		scanPage("p4", scanItem("same-b", "2026-09-08T10:00:00Z", "same b"), scanItem("older-1", "2026-09-08T09:00:00Z", "older 1")),
		scanPage("p5", scanItem("older-2", "2026-09-08T08:00:00Z", "older 2")),
		scanPage("p6", scanItem("older-3", "2026-09-08T07:00:00Z", "older 3")),
	}
	for index, page := range pages {
		if err := scan.AddPage(page); err != nil {
			t.Fatalf("page %d: %v", index+1, err)
		}
		if index < len(pages)-1 && scan.Complete() {
			t.Fatalf("scan stopped before two pages beyond the complete equal-time group: page %d", index+1)
		}
	}
	if !scan.Complete() || scan.StopReason() != "safe_boundary" || !scan.Quality().MayAdvanceCheckpoint() {
		t.Fatalf("scan did not close safe boundary: complete=%v reason=%s quality=%+v", scan.Complete(), scan.StopReason(), scan.Quality())
	}
	candidate, err := scan.CheckpointCandidate()
	if err != nil || candidate.Version != 8 || candidate.LastActivityAt != "2026-09-08T12:00:00Z" || fmt.Sprint(candidate.FrontierKeys) != "[new-2 new-1 same-a]" {
		t.Fatalf("checkpoint candidate=%+v err=%v", candidate, err)
	}
}

func TestFrontierScanRequiresEveryOldKeyThenOverlap(t *testing.T) {
	spec := scanSpec()
	spec.Listing.BoundaryMode = "frontier_keys"
	spec.Listing.ActivityField = ""
	checkpoint := &recipeabi.CheckpointRef{Version: 2, FrontierKeys: []string{"old-a", "old-b"}}
	scan, err := NewListingScan(spec, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []DocumentResult{
		scanPage("p2", scanItem("new", "", "new"), scanItem("old-a", "", "old a")),
		scanPage("p3", scanItem("other", "", "other")),
		scanPage("p4", scanItem("old-b", "", "old b")),
		scanPage("p5", scanItem("overlap-1", "", "overlap 1")),
		scanPage("p6", scanItem("overlap-2", "", "overlap 2")),
	} {
		if err := scan.AddPage(page); err != nil {
			t.Fatal(err)
		}
	}
	if !scan.Quality().MayAdvanceCheckpoint() || scan.StopReason() != "safe_boundary" {
		t.Fatalf("frontier scan proof=%+v reason=%s", scan.Quality(), scan.StopReason())
	}
}

func TestListingScanDoesNotAdvanceWhenBoundaryDisappearsOrDuplicateChanges(t *testing.T) {
	spec := scanSpec()
	spec.Listing.MaxPages = 2
	checkpoint := &recipeabi.CheckpointRef{Version: 1, LastActivityAt: "2026-09-01T00:00:00Z"}
	scan, _ := NewListingScan(spec, checkpoint)
	_ = scan.AddPage(scanPage("p2", scanItem("same", "2026-09-08T10:00:00Z", "first")))
	_ = scan.AddPage(scanPage("p3", scanItem("same", "2026-09-08T10:00:00Z", "changed")))
	if !scan.Complete() || scan.StopReason() != "max_pages" || scan.Quality().PaginationStable || scan.Quality().MayAdvanceCheckpoint() {
		t.Fatalf("unsafe scan was accepted: reason=%s quality=%+v", scan.StopReason(), scan.Quality())
	}
	if _, err := scan.CheckpointCandidate(); err == nil {
		t.Fatal("unsafe scan produced a checkpoint candidate")
	}
}

func TestBaselineAdvancesOnlyAtEndOfInput(t *testing.T) {
	scan, err := NewListingScan(scanSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := scan.AddPage(scanPage("p2", scanItem("job-2", "2026-09-08T10:00:00Z", "two"))); err != nil || scan.Complete() {
		t.Fatalf("baseline ended early: %v", err)
	}
	if err := scan.AddPage(scanPage("", scanItem("job-1", "2026-09-08T09:00:00Z", "one"))); err != nil {
		t.Fatal(err)
	}
	if !scan.Quality().MayAdvanceCheckpoint() || scan.StopReason() != "end_of_input" {
		t.Fatalf("baseline proof=%+v reason=%s", scan.Quality(), scan.StopReason())
	}
}

func TestListingScanReleasesStreamedBodiesButRetainsDedupeProof(t *testing.T) {
	scan, err := NewListingScan(scanSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first := scanItem("job-1", "2026-09-08T10:00:00Z", "one")
	if err := scan.AddPage(scanPage("p2", first)); err != nil {
		t.Fatal(err)
	}
	if scan.ItemCount() != 1 || scan.BufferedItemCount() != 1 {
		t.Fatalf("first page counts total=%d buffered=%d", scan.ItemCount(), scan.BufferedItemCount())
	}
	scan.DiscardBufferedItems()
	if scan.ItemCount() != 1 || scan.BufferedItemCount() != 0 || len(scan.Items()) != 0 {
		t.Fatalf("discard changed totals or retained bodies: total=%d buffered=%d", scan.ItemCount(), scan.BufferedItemCount())
	}
	if err := scan.AddPage(scanPage("p3", first, scanItem("job-2", "2026-09-08T09:00:00Z", "two"))); err != nil {
		t.Fatal(err)
	}
	if scan.ItemCount() != 2 || scan.BufferedItemCount() != 1 || !scan.Quality().PaginationStable {
		t.Fatalf("fingerprint dedupe after discard: total=%d buffered=%d quality=%+v",
			scan.ItemCount(), scan.BufferedItemCount(), scan.Quality())
	}
	if err := scan.AddPage(scanPage("p4", scanItem("job-1", "2026-09-08T10:00:00Z", "changed"))); err != nil {
		t.Fatal(err)
	}
	if scan.Quality().PaginationStable {
		t.Fatal("changed duplicate was not detected after its body was released")
	}
}

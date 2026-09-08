package recruitingexecutor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func listingResultItem(t *testing.T, id any, detailURL, activity string) map[string]json.RawMessage {
	t.Helper()
	item := map[string]json.RawMessage{}
	for name, value := range map[string]any{"id": id, "detail_url": detailURL} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		item[name] = raw
	}
	if activity != "" {
		item["activity_at"], _ = json.Marshal(activity)
	}
	return item
}

func TestPrepareListingSubmissionsBuildsBoundedPagesAndCompletion(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, spec, _ := listingExecutionOffer(t, now)
	spec.Listing.ActivityField = "activity_at"
	spec.Extraction.Fields["activity_at"] = "/activity_at"
	writer := &writeHandleStub{}
	resources := &artifactCreatorStub{writer: writer}
	sink, err := newAtollArtifactSink(resources, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "artifacts", WorkID: offer.Work.WorkID, AttemptID: offer.Attempt.AttemptID,
		AccessScope: "operators", Retention: "30d", MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	pageOne := recipeabi.ArtifactRef{ArtifactID: "page-1", ContentHash: "sha256:" + sixtyFourZeros,
		ObjectRef: "daemon://worker-a/recruiting/artifacts/page-1.bin"}
	pageTwo := recipeabi.ArtifactRef{ArtifactID: "page-2", ContentHash: "sha256:" + sixtyFourOnes,
		ObjectRef: "daemon://worker-a/recruiting/artifacts/page-2.bin"}
	quality := recipeabi.QualityProof{IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true,
		PreviousFrontierReached: true, OverlapCompleted: true, ItemCount: 3}
	checkpoint := &recipeabi.CheckpointRef{Version: 4, FrontierKeys: []string{"101", "job-2"}, LastActivityAt: "2026-09-08T11:00:00Z"}
	run := httpdriver.ListingRunResult{
		Output: recipeabi.RunOutput{ABIVersion: recipeabi.Version, AttemptID: offer.Attempt.AttemptID,
			Artifacts: []recipeabi.ArtifactRef{pageOne, pageTwo}, Result: json.RawMessage(`{"items":3}`), Quality: quality},
		CheckpointCandidate: checkpoint,
		Pages: []httpdriver.ListingPage{
			{Sequence: 1, URL: "https://jobs.example.com/openings", ResumeCursor: "https://jobs.example.com/openings?page=2",
				Artifact: pageOne, Items: []map[string]json.RawMessage{
					listingResultItem(t, 101, "/roles/101", "2026-09-08T11:00:00Z"),
					listingResultItem(t, "job-2", "https://detail.example.net/roles/2", "2026-09-08T10:00:00Z")}},
			{Sequence: 2, URL: "https://jobs.example.com/openings?page=2", Terminal: true,
				Artifact: pageTwo, Items: []map[string]json.RawMessage{listingResultItem(t, "old", "/roles/old", "2026-09-08T09:00:00Z")}},
		},
	}
	submissions, err := prepareListingSubmissions(context.Background(), offer, spec, run, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(submissions.Pages) != 2 || len(submissions.Pages[0].Observations) != 2 || !submissions.Pages[1].Terminal ||
		submissions.Pages[0].Artifact.WorkID != offer.Work.WorkID || submissions.Pages[0].Artifact.AttemptID != offer.Attempt.AttemptID ||
		submissions.Pages[0].Observations[0].SourceJobKey != "101" ||
		submissions.Pages[0].Observations[0].DetailURL != "https://jobs.example.com/roles/101" ||
		submissions.Pages[0].Observations[1].DetailURL != "https://detail.example.net/roles/2" {
		t.Fatalf("unexpected listing page submissions: %+v", submissions.Pages)
	}
	if submissions.Completion.Quality.ItemCount != 3 || submissions.Completion.Artifact.Kind != "listing_delta" ||
		submissions.Completion.Checkpoint.FrontierActivityAt != checkpoint.LastActivityAt || !writer.committed {
		t.Fatalf("unexpected listing completion: %+v committed=%v", submissions.Completion, writer.committed)
	}
}

func TestPrepareListingSubmissionsRejectsCountAndPageContractMismatch(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, spec, _ := listingExecutionOffer(t, now)
	resources := &artifactCreatorStub{writer: &writeHandleStub{}}
	sink, _ := newAtollArtifactSink(resources, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "artifacts", WorkID: offer.Work.WorkID, AttemptID: offer.Attempt.AttemptID,
		AccessScope: "operators", Retention: "30d", MaxBytes: 4096})
	page := recipeabi.ArtifactRef{ArtifactID: "page", ContentHash: "sha256:" + sixtyFourZeros,
		ObjectRef: "daemon://worker-a/recruiting/artifacts/page.bin"}
	run := httpdriver.ListingRunResult{Output: recipeabi.RunOutput{AttemptID: offer.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{page}, Result: json.RawMessage(`{}`), Quality: recipeabi.QualityProof{ItemCount: 2}},
		CheckpointCandidate: &recipeabi.CheckpointRef{Version: 4, FrontierKeys: []string{"job"}},
		Pages: []httpdriver.ListingPage{{Sequence: 1, URL: "https://jobs.example.com/openings", Terminal: true,
			Artifact: page, Items: []map[string]json.RawMessage{listingResultItem(t, "job", "/job", "")}}}}
	if _, err := prepareListingSubmissions(context.Background(), offer, spec, run, sink); err == nil {
		t.Fatal("expected unique observation count mismatch")
	}
	run.Output.Quality.ItemCount = 1
	run.Pages[0].Sequence = 2
	if _, err := prepareListingSubmissions(context.Background(), offer, spec, run, sink); err == nil {
		t.Fatal("expected page sequence mismatch")
	}
}

const (
	sixtyFourZeros = "0000000000000000000000000000000000000000000000000000000000000000"
	sixtyFourOnes  = "1111111111111111111111111111111111111111111111111111111111111111"
)

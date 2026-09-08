package recruitingexecutor

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestPrepareDetailSubmissionBindsArtifactJobGenerationAndRecipe(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, _ := detailExecutionOffer(t, now)
	resources := &artifactCreatorStub{writer: &writeHandleStub{}}
	sink, _ := newAtollArtifactSink(resources, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "artifacts", WorkID: offer.Work.WorkID, AttemptID: offer.Attempt.AttemptID,
		AccessScope: "operators", Retention: "30d", MaxBytes: 4096})
	detail := []byte(`{"location":"Remote","title":"Engineer"}`)
	sum := sha256.Sum256(detail)
	ref := recipeabi.ArtifactRef{ArtifactID: "response-1", ContentHash: "sha256:" + sixtyFourZeros,
		ObjectRef: "daemon://worker-a/recruiting/artifacts/response-1.bin"}
	run := httpdriver.DetailRunResult{Output: recipeabi.RunOutput{AttemptID: offer.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{ref}, Result: detail}, ResponseArtifact: ref,
		NormalizedContentHash: fmt.Sprintf("sha256:%x", sum), Detail: detail}
	result, err := prepareDetailSubmission(offer, run, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.AttemptID != offer.Attempt.AttemptID || result.ExecutorIncarnation != offer.Attempt.ExecutorIncarnation ||
		result.Artifact.WorkID != offer.Work.WorkID || result.Artifact.AttemptID != offer.Attempt.AttemptID ||
		result.DetailVersionID == "" || result.NormalizedContentHash != run.NormalizedContentHash || string(result.Detail) != string(detail) {
		t.Fatalf("unexpected detail submission: %+v", result)
	}
	again, err := prepareDetailSubmission(offer, run, sink)
	if err != nil || again.DetailVersionID != result.DetailVersionID {
		t.Fatalf("detail submission identity changed: %+v err=%v", again, err)
	}
}

func TestPrepareDetailSubmissionRejectsHashAndAttemptMismatch(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	offer, _, _ := detailExecutionOffer(t, now)
	resources := &artifactCreatorStub{writer: &writeHandleStub{}}
	sink, _ := newAtollArtifactSink(resources, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "artifacts", WorkID: offer.Work.WorkID, AttemptID: offer.Attempt.AttemptID,
		AccessScope: "operators", Retention: "30d", MaxBytes: 4096})
	ref := recipeabi.ArtifactRef{ArtifactID: "response", ContentHash: "sha256:" + sixtyFourZeros, ObjectRef: "memory://response"}
	run := httpdriver.DetailRunResult{Output: recipeabi.RunOutput{AttemptID: offer.Attempt.AttemptID,
		Artifacts: []recipeabi.ArtifactRef{ref}}, ResponseArtifact: ref, NormalizedContentHash: "sha256:" + sixtyFourZeros,
		Detail: []byte(`{"title":"Engineer"}`)}
	if _, err := prepareDetailSubmission(offer, run, sink); err == nil {
		t.Fatal("expected normalized hash rejection")
	}
	run.Output.AttemptID = "other-attempt"
	if _, err := prepareDetailSubmission(offer, run, sink); err == nil {
		t.Fatal("expected attempt mismatch rejection")
	}
}

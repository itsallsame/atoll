package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDetailResultRejectsUnboundedStructuredPayloadBeforeDatabase(t *testing.T) {
	artifact, _ := model.NewArtifactMetadata("artifact", model.ArtifactResponse, "sha256:raw", "artifact://detail/result",
		"work", "attempt", "operators", "30d", true)
	_, err := (&Repository{}).AcceptDetailResult(t.Context(), DetailResult{
		AttemptID: "attempt", ExecutorActorID: "executor", ExecutorIncarnation: "boot", Artifact: artifact,
		DetailVersionID: "version", NormalizedContentHash: "sha256:normalized",
		DetailJSON: json.RawMessage(`{"value":"` + strings.Repeat("x", detailResultMaxJSONBytes) + `"}`),
		ObservedAt: time.Now(), CauseCommandID: "command",
	})
	if err == nil {
		t.Fatal("oversized detail result reached repository transaction")
	}
}

func TestSuccessfulResultSupportingArtifactsAreTraceOnlyAndBounded(t *testing.T) {
	primary, _ := model.NewArtifactMetadata("response", model.ArtifactResponse, "sha256:raw", "artifact://detail/response",
		"work", "attempt", "operators", "30d", true)
	trace, _ := model.NewArtifactMetadata("trace", model.ArtifactTrace, "sha256:trace", "artifact://detail/trace",
		"work", "attempt", "operators", "30d", true)
	if err := validateSupportingResultArtifacts(primary, []model.ArtifactMetadata{trace}, "attempt"); err != nil {
		t.Fatal(err)
	}
	for _, supporting := range [][]model.ArtifactMetadata{
		{primary},
		{func() model.ArtifactMetadata { value := trace; value.WorkID = "other-work"; return value }()},
		{func() model.ArtifactMetadata { value := trace; value.Kind = model.ArtifactFailure; return value }()},
		make([]model.ArtifactMetadata, 10),
	} {
		if err := validateSupportingResultArtifacts(primary, supporting, "attempt"); err == nil {
			t.Fatalf("unsafe supporting Artifacts were accepted: %+v", supporting)
		}
	}
}

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

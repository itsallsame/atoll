package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	recruitingbridge "github.com/wanpengxie/atoll/tools/recruiting-extension/bridge"
)

func TestRecruitingExtensionCaptureLogicAgainstRealDiscordPage(t *testing.T) {
	if os.Getenv("ATOLL_RECRUITING_EXTENSION_LIVE") != "1" {
		t.Skip("set ATOLL_RECRUITING_EXTENSION_LIVE=1 for the real Chrome website test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "../scripts/recruiting-extension-live-capture.mjs")
	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("real Chrome capture: %v: %s", err, exitErr.Stderr)
		}
		t.Fatal(err)
	}
	var result struct {
		Draft        json.RawMessage `json:"draft"`
		Collection   string          `json:"collection"`
		MatchedCount int             `json:"matchedCount"`
		FirstHref    string          `json:"firstHref"`
		FirstTitle   string          `json:"firstTitle"`
		Detail       struct {
			Draft             json.RawMessage `json:"draft"`
			PageURL           string          `json:"pageURL"`
			Title             string          `json:"title"`
			DescriptionLength int             `json:"descriptionLength"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode real Chrome result: %v: %s", err, output)
	}
	draft, err := recruitingbridge.DecodeDraft(result.Draft)
	if err != nil {
		t.Fatal(err)
	}
	_, err = recruitingbridge.Build(draft, recruitingbridge.SourceFence{SourceID: draft.SourceID,
		SourceVersion: 1, EndpointRevision: 1, EndpointURL: draft.PageURL,
		ReadinessStatus: model.SourceReady, ControlStatus: model.ControlActive, HealthStatus: model.HealthHealthy},
		"human:live-extension:1", time.Now().UTC())
	if err != nil || result.Collection != "tr.job-post" || result.MatchedCount < 2 || result.FirstHref == "" || result.FirstTitle == "" {
		t.Fatalf("real Capture result=%+v buildErr=%v", result, err)
	}
	detailDraft, err := recruitingbridge.DecodeDraft(result.Detail.Draft)
	if err != nil {
		t.Fatal(err)
	}
	_, err = recruitingbridge.Build(detailDraft, recruitingbridge.SourceFence{SourceID: detailDraft.SourceID,
		SourceVersion: 1, EndpointRevision: 1, EndpointURL: draft.PageURL,
		SampleJobID: detailDraft.SampleJobID, SampleJobVersion: 1, SampleJobURL: result.Detail.PageURL,
		ReadinessStatus: model.SourceReady, ControlStatus: model.ControlActive, HealthStatus: model.HealthHealthy},
		"human:live-extension:1", time.Now().UTC())
	if err != nil || result.Detail.Title == "" || result.Detail.DescriptionLength < 100 {
		t.Fatalf("real Detail Capture result=%+v buildErr=%v", result.Detail, err)
	}
}

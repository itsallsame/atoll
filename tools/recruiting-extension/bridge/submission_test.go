package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type submissionFake struct {
	source    model.RecruitmentSource
	sender    string
	resources []string
	proposal  map[string]any
	failAt    string
}

func (f *submissionFake) Request(_ context.Context, word string, payload any) (map[string]any, string, error) {
	if f.failAt == word {
		return nil, f.sender, fmt.Errorf("injected %s failure", word)
	}
	switch word {
	case "recruiting.source.get":
		return map[string]any{"status": "completed", "entity": f.source}, f.sender, nil
	case "recruiting.recipe.propose":
		raw, _ := json.Marshal(payload)
		_ = json.Unmarshal(raw, &f.proposal)
		return map[string]any{"status": "completed", "recipe": map[string]any{"status": "draft"}}, f.sender, nil
	default:
		return nil, f.sender, fmt.Errorf("unexpected word %q", word)
	}
}

func (f *submissionFake) EnsureResource(_ context.Context, resourceID string, _ json.RawMessage) error {
	if f.failAt == resourceID {
		return fmt.Errorf("injected Resource failure")
	}
	f.resources = append(f.resources, resourceID)
	return nil
}

func readyBridgeSource() model.RecruitmentSource {
	return model.RecruitmentSource{SourceID: "source-01", CompanyID: "company-01", ReadinessStatus: model.SourceReady,
		ControlStatus: model.ControlActive, HealthStatus: model.HealthHealthy, Version: 9,
		ActiveEndpoint: &model.SourceEndpoint{URL: "https://jobs.example.test/openings", Revision: 3,
			CanonicalKey: "https://jobs.example.test/openings"}}
}

func TestSubmitDraftUsesPublicResourcesAndRecipeCommandOnly(t *testing.T) {
	fake := &submissionFake{source: readyBridgeSource(), sender: "human:operator:7"}
	result, err := SubmitDraft(context.Background(), fake, bridgeDraft(), func() time.Time {
		return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	wantResources := []string{"artifact://recruiting-capture/capture-01/page",
		"recipe://recruiting-capture/capture-01/candidate", "artifact://recruiting-capture/capture-01/capture"}
	if !reflect.DeepEqual(fake.resources, wantResources) || fake.proposal["capture_ref"] != wantResources[2] ||
		result.CaptureID != "capture-01" {
		t.Fatalf("submission resources=%v proposal=%v result=%+v", fake.resources, fake.proposal, result)
	}
}

func TestSubmitDraftStopsBeforeProposalWhenAResourceFails(t *testing.T) {
	fake := &submissionFake{source: readyBridgeSource(), sender: "human:operator:7",
		failAt: "recipe://recruiting-capture/capture-01/candidate"}
	if _, err := SubmitDraft(context.Background(), fake, bridgeDraft(), func() time.Time {
		return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	}); err == nil {
		t.Fatal("partial Resource upload incorrectly proceeded to proposal")
	}
	if fake.proposal != nil || !reflect.DeepEqual(fake.resources, []string{"artifact://recruiting-capture/capture-01/page"}) {
		t.Fatalf("submission continued after failure: resources=%v proposal=%v", fake.resources, fake.proposal)
	}
}

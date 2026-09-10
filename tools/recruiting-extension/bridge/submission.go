package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type atollSubmissionClient interface {
	Request(context.Context, string, any) (map[string]any, string, error)
	EnsureResource(context.Context, string, json.RawMessage) error
}

type SubmissionResult struct {
	CaptureID string         `json:"capture_id"`
	RecipeID  string         `json:"recipe_id"`
	Version   uint64         `json:"recipe_version"`
	Response  map[string]any `json:"response"`
}

// SubmitDraft resolves authoritative Source and sender facts first, then
// uploads immutable evidence/Recipe/Capture Resources before invoking the
// existing recruiting.recipe.propose command. It never writes MySQL directly.
func SubmitDraft(ctx context.Context, client atollSubmissionClient, draft Draft, now func() time.Time) (SubmissionResult, error) {
	if client == nil || now == nil {
		return SubmissionResult{}, fmt.Errorf("submission requires an Atoll client and clock")
	}
	terminal, sender, err := client.Request(ctx, "recruiting.source.get", map[string]string{"id": draft.SourceID})
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("read Source fence: %w", err)
	}
	entity, exists := terminal["entity"]
	if !exists || sender == "" {
		return SubmissionResult{}, fmt.Errorf("Source response omitted entity or authenticated sender")
	}
	rawSource, err := json.Marshal(entity)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("encode Source response: %w", err)
	}
	var source model.RecruitmentSource
	if err := json.Unmarshal(rawSource, &source); err != nil {
		return SubmissionResult{}, fmt.Errorf("decode active Source response: %w", err)
	}
	if source.ActiveEndpoint == nil {
		return SubmissionResult{}, fmt.Errorf("Source response has no active Endpoint")
	}
	fence := SourceFence{SourceID: source.SourceID, SourceVersion: source.Version,
		EndpointRevision: source.ActiveEndpoint.Revision, EndpointURL: source.ActiveEndpoint.URL,
		ReadinessStatus: source.ReadinessStatus, ControlStatus: source.ControlStatus, HealthStatus: source.HealthStatus}
	if draft.Candidate.Kind == recipeabi.KindDetail {
		terminal, jobSender, jobErr := client.Request(ctx, "recruiting.job.get", map[string]string{"id": draft.SampleJobID})
		if jobErr != nil {
			return SubmissionResult{}, fmt.Errorf("read Detail sample Job fence: %w", jobErr)
		}
		if jobSender != sender {
			return SubmissionResult{}, fmt.Errorf("authenticated sender changed while reading Detail sample Job")
		}
		rawJob, marshalErr := json.Marshal(terminal["entity"])
		var job model.SourceJob
		if marshalErr != nil || json.Unmarshal(rawJob, &job) != nil || job.SourceID != source.SourceID {
			return SubmissionResult{}, fmt.Errorf("Detail sample Job does not belong to the Source")
		}
		fence.SampleJobID, fence.SampleJobVersion, fence.SampleJobURL = job.JobID, job.Version, job.DetailURL
	}
	resources, err := Build(draft, fence, sender, now().UTC())
	if err != nil {
		return SubmissionResult{}, err
	}
	for _, item := range []struct {
		name string
		ref  string
		body json.RawMessage
	}{{"evidence", resources.EvidenceRef, resources.EvidenceBody}, {"Recipe", resources.RecipeRef, resources.RecipeBody},
		{"Capture", resources.CaptureRef, resources.CaptureBody}} {
		if err := client.EnsureResource(ctx, item.ref, item.body); err != nil {
			return SubmissionResult{}, fmt.Errorf("upload %s Resource: %w", item.name, err)
		}
	}
	response, responseSender, err := client.Request(ctx, "recruiting.recipe.propose", resources.Proposal)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("propose captured Recipe: %w", err)
	}
	if responseSender != sender {
		return SubmissionResult{}, fmt.Errorf("authenticated sender changed during Extension proposal")
	}
	return SubmissionResult{CaptureID: draft.CaptureID, RecipeID: draft.RecipeID,
		Version: draft.RecipeVersion, Response: response}, nil
}

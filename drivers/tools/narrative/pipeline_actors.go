package narrative

import (
	"encoding/json"
	"fmt"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/message"
	"github.com/wanpengxie/atoll/protocol/resource"
)

const (
	plannerStateKey resource.ResourceID = "narrative.plans"
	writerStateKey  resource.ResourceID = "narrative.drafts"
	criticStateKey  resource.ResourceID = "narrative.reviews"
)

type plannerState struct {
	Plans map[string]chapterPlan `json:"plans"`
}
type writerState struct {
	Drafts map[string][]chapter `json:"drafts"`
}
type criticState struct {
	Reviews map[string][]chapterReview `json:"reviews"`
}

func plannerDef() actorbase.Def {
	return actorbase.Def{Manifest: plannerManifest(), New: func() (actorbase.Proc, error) { return runPlanner, nil }}
}
func writerDef(generator proseGenerator) actorbase.Def {
	return actorbase.Def{Manifest: writerManifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return runWriter(sys, generator) }, nil
	}}
}
func criticDef(critic proseCritic) actorbase.Def {
	return actorbase.Def{Manifest: criticManifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return runCritic(sys, critic) }, nil
	}}
}

func runPlanner(sys actorbase.Sys) error {
	state := plannerState{Plans: map[string]chapterPlan{}}
	if err := recoverPipelineState(sys, plannerStateKey, &state); err != nil {
		return fmt.Errorf("narrative-planner: %w", err)
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			continue
		}
		if msg.Type != TypePlan {
			_, _ = sys.Fail(msg, "type_unsupported", "narrative planner does not handle "+msg.Type)
			continue
		}
		var payload planRequest
		if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil || payload.StartDay < 1 || payload.EndDay < payload.StartDay || len(payload.Events) == 0 {
			_, _ = sys.Fail(msg, "bad_payload", "ordered source events and a valid day range are required")
			continue
		}
		plan := planChapter(payload.StartDay, payload.EndDay, payload.Events)
		state.Plans[chapterJobID(payload.Events)] = plan
		if err := putJSON(sys, plannerStateKey, state); err != nil {
			_, _ = sys.Fail(msg, "state_failed", err.Error())
			continue
		}
		_, _ = sys.Reply(msg, plan)
	}
}

func runWriter(sys actorbase.Sys, generator proseGenerator) error {
	state := writerState{Drafts: map[string][]chapter{}}
	if err := recoverPipelineState(sys, writerStateKey, &state); err != nil {
		return fmt.Errorf("narrative-writer: %w", err)
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			continue
		}
		if msg.Type != TypeWrite {
			_, _ = sys.Fail(msg, "type_unsupported", "narrative writer does not handle "+msg.Type)
			continue
		}
		var payload writeRequest
		if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil || payload.JobID == "" || payload.Attempt < 1 || payload.Attempt > 3 || len(payload.Events) == 0 {
			_, _ = sys.Fail(msg, "bad_payload", "job_id, attempt 1..3, plan and source events are required")
			continue
		}
		draft := projectChapter(payload.Plan.StartDay, payload.Plan.EndDay, payload.Events)
		if generator != nil && payload.Plan.Ready {
			generated, generationErr := generator.Generate(msg.Ctx(), payload)
			if generationErr != nil {
				_, _ = sys.Fail(msg, "generation_failed", generationErr.Error())
				continue
			}
			draft = chapter{
				Day: payload.Plan.EndDay, StartDay: payload.Plan.StartDay, EndDay: payload.Plan.EndDay,
				Title: generated.Title, POV: generated.POV, Markdown: generated.Markdown,
				SourceEvents: eventIDs(payload.Events), Claims: chapterClaims(payload.Events),
				Generation: "model", Realizations: generated.FactRealizations, Status: "draft",
			}
		}
		draft.DraftID, draft.Version, draft.Plan = payload.JobID, payload.Attempt, payload.Plan
		state.Drafts[payload.JobID] = append(state.Drafts[payload.JobID], draft)
		if err := putJSON(sys, writerStateKey, state); err != nil {
			_, _ = sys.Fail(msg, "state_failed", err.Error())
			continue
		}
		_, _ = sys.Reply(msg, draft)
	}
}

func runCritic(sys actorbase.Sys, critic proseCritic) error {
	state := criticState{Reviews: map[string][]chapterReview{}}
	if err := recoverPipelineState(sys, criticStateKey, &state); err != nil {
		return fmt.Errorf("narrative-critic: %w", err)
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			continue
		}
		if msg.Type != TypeCritique {
			_, _ = sys.Fail(msg, "type_unsupported", "narrative critic does not handle "+msg.Type)
			continue
		}
		var payload critiqueRequest
		if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil || payload.Draft.DraftID == "" || len(payload.Events) == 0 {
			_, _ = sys.Fail(msg, "bad_payload", "plan, source events and a versioned draft are required")
			continue
		}
		review := reviewProjectedChapter(payload.Draft, payload.Plan, payload.Events)
		review.Reviewers = []string{"deterministic"}
		if review.Pass && critic != nil && payload.Draft.Generation == "model" {
			issues, critiqueErr := critic.Critique(msg.Ctx(), payload.Plan, payload.Events, payload.Draft)
			if critiqueErr != nil {
				_, _ = sys.Fail(msg, "critique_failed", critiqueErr.Error())
				continue
			}
			review.Issues = append(review.Issues, issues...)
			review.Pass = len(review.Issues) == 0
			review.Reviewers = append(review.Reviewers, "model")
		}
		review.DraftID, review.Version = payload.Draft.DraftID, payload.Draft.Version
		state.Reviews[payload.Draft.DraftID] = append(state.Reviews[payload.Draft.DraftID], review)
		if err := putJSON(sys, criticStateKey, state); err != nil {
			_, _ = sys.Fail(msg, "state_failed", err.Error())
			continue
		}
		_, _ = sys.Reply(msg, review)
	}
}

func recoverPipelineState(sys actorbase.Sys, key resource.ResourceID, target any) error {
	out, err := sys.State().Get(key)
	if err != nil {
		return err
	}
	if out.Found {
		return json.Unmarshal(out.Value, target)
	}
	return nil
}

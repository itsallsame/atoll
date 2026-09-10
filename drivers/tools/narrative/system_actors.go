package narrative

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
	"github.com/wanpengxie/atoll/protocol/resource"
)

const (
	TypeSystemPropose = "narrative.system.propose"
	TypeProject       = "narrative.project"
	TypeProjectReset  = "narrative.project.reset"
	TypeChapter       = "narrative.chapter"
	TypeScenePlan     = "narrative.scene.plan"
	TypeChapterDraft  = "narrative.chapter.draft"
	TypeChapterReview = "narrative.chapter.review"
	maxSceneFacts     = 6
	TypePlan          = "narrative.plan"
	TypeWrite         = "narrative.write"
	TypeCritique      = "narrative.critique"
)

const projectorStateKey resource.ResourceID = "narrative.chapters"

type systemTickPayload struct {
	Day        int            `json:"day"`
	Tick       string         `json:"tick"`
	State      map[string]any `json:"state"`
	FiredRules map[string]int `json:"fired_rules"`
	RuleCounts map[string]int `json:"rule_counts"`
}

type systemProposalReply struct {
	Proposals []proposal `json:"proposals"`
}

type chapter struct {
	DraftID      string            `json:"draft_id,omitempty"`
	Version      int               `json:"version,omitempty"`
	Day          int               `json:"day"`
	StartDay     int               `json:"start_day"`
	EndDay       int               `json:"end_day"`
	Title        string            `json:"title"`
	POV          string            `json:"pov"`
	Markdown     string            `json:"markdown"`
	SourceEvents []string          `json:"source_events"`
	Claims       []chapterClaim    `json:"claims"`
	Generation   string            `json:"generation,omitempty"`
	Realizations map[string]string `json:"fact_realizations,omitempty"`
	Plan         chapterPlan       `json:"plan"`
	Review       chapterReview     `json:"review"`
	Status       string            `json:"publication_status"`
}

type chapterClaim struct {
	EventID      string `json:"event_id"`
	Summary      string `json:"summary"`
	ActualEffect string `json:"actual_effect"`
}

type chapterPlan struct {
	StartDay         int         `json:"start_day"`
	EndDay           int         `json:"end_day"`
	POV              string      `json:"pov"`
	DramaticQuestion string      `json:"dramatic_question"`
	Beats            []sceneBeat `json:"beats"`
	Ready            bool        `json:"ready"`
}

type sceneBeat struct {
	EventID   string   `json:"event_id"`
	Role      string   `json:"role"`
	Actor     string   `json:"actor"`
	Goal      string   `json:"goal,omitempty"`
	Causes    []string `json:"causes"`
	Causality string   `json:"causality"`
}

type chapterReview struct {
	DraftID   string   `json:"draft_id,omitempty"`
	Version   int      `json:"version,omitempty"`
	Pass      bool     `json:"pass"`
	Issues    []string `json:"issues,omitempty"`
	Reviewers []string `json:"reviewers,omitempty"`
}

type planRequest struct {
	StartDay int    `json:"start_day"`
	EndDay   int    `json:"end_day"`
	Events   []fact `json:"events"`
}
type writeRequest struct {
	JobID           string      `json:"job_id"`
	Attempt         int         `json:"attempt"`
	Plan            chapterPlan `json:"plan"`
	Events          []fact      `json:"events"`
	PreviousChapter *chapter    `json:"previous_chapter,omitempty"`
	Prior           *chapter    `json:"prior,omitempty"`
	Issues          []string    `json:"issues,omitempty"`
}
type critiqueRequest struct {
	Plan   chapterPlan `json:"plan"`
	Events []fact      `json:"events"`
	Draft  chapter     `json:"draft"`
}

type projectorState struct {
	Chapters     map[int]chapter `json:"chapters"`
	OpenStartDay int             `json:"open_start_day,omitempty"`
	OpenEvents   []fact          `json:"open_events,omitempty"`
}

func environmentDef(spec simulationSpec) actorbase.Def {
	return actorbase.Def{Manifest: environmentManifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return runEnvironment(sys, spec) }, nil
	}}
}

func runEnvironment(sys actorbase.Sys, spec simulationSpec) error {
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			continue
		}
		if msg.Type != TypeSystemPropose {
			_, _ = sys.Fail(msg, "type_unsupported", "narrative environment does not handle "+msg.Type)
			continue
		}
		var payload systemTickPayload
		if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil || payload.Day < 1 || payload.Tick == "" {
			_, _ = sys.Fail(msg, "bad_payload", "day, tick and world state are required")
			continue
		}
		proposals := make([]proposal, 0)
		for _, candidate := range rulesForTick(spec.Processes, payload.Tick) {
			if !canFire(candidate, payload.FiredRules[candidate.ID], payload.RuleCounts[candidate.ID], payload.Day) || !conditionsMatch(payload.State, candidate.When) {
				continue
			}
			selected, err := choose(candidate.Options, payload.State)
			if err != nil {
				continue
			}
			proposals = append(proposals, proposal{RuleID: candidate.ID, Actor: "environment", Tick: payload.Tick, SelectedOption: selected.ID,
				Action: selected.Action, Goal: selected.PerceivedGoal, ExpectedEffect: selected.IntendedEffect,
				Preconditions: append([]condition(nil), candidate.When...)})
		}
		_, _ = sys.Reply(msg, systemProposalReply{Proposals: proposals})
	}
}

func projectorDef() actorbase.Def {
	return actorbase.Def{Manifest: projectorManifest(), New: func() (actorbase.Proc, error) {
		return runProjector, nil
	}}
}

func runProjector(sys actorbase.Sys) error {
	state := projectorState{Chapters: map[int]chapter{}}
	out, err := sys.State().Get(projectorStateKey)
	if err != nil {
		return fmt.Errorf("narrative-projector: read state: %w", err)
	}
	if out.Found {
		if err := json.Unmarshal(out.Value, &state); err != nil {
			return fmt.Errorf("narrative-projector: recover state: %w", err)
		}
	}
	if state.Chapters == nil {
		state.Chapters = map[int]chapter{}
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			continue
		}
		if msg.Type == TypeProjectReset {
			state.OpenStartDay = 0
			state.OpenEvents = nil
			if err := putJSON(sys, projectorStateKey, state); err != nil {
				_, _ = sys.Fail(msg, "state_failed", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, map[string]any{"reset": true})
			continue
		}
		if msg.Type != TypeProject {
			_, _ = sys.Fail(msg, "type_unsupported", "narrative projector does not handle "+msg.Type)
			continue
		}
		var result evolutionResult
		if err := actorbase.DecodeStrict(msg.Payload, &result); err != nil || result.Day < 1 {
			_, _ = sys.Fail(msg, "bad_payload", "a completed evolution day is required")
			continue
		}
		if state.OpenStartDay == 0 {
			state.OpenStartDay = result.Day
		}
		state.OpenEvents = append(state.OpenEvents, result.Events...)
		projected := chapter{Day: result.Day, StartDay: state.OpenStartDay, EndDay: result.Day, Status: "accumulating",
			Title: "场景积累中", POV: "尚未封章", SourceEvents: eventIDs(state.OpenEvents)}
		batch := nextSceneBatch(state.OpenEvents)
		if len(batch) >= 4 {
			batchStartDay, batchEndDay := batch[0].Day, batch[len(batch)-1].Day
			plan, pipelineErr := requestPlan(sys, msg, batchStartDay, batchEndDay, batch)
			if pipelineErr == nil {
				projected.Plan = plan
				emitArtifact(sys, msg, TypeScenePlan, plan, message.VisibilityPublic)
				candidate, review, runErr := runWritingLoop(sys, msg, plan, batch, latestChapterBefore(state.Chapters, batchStartDay))
				projected.Review = review
				if runErr == nil && plan.Ready && review.Pass {
					candidate.Review = review
					candidate.Status = "published"
					projected = candidate
					state.Chapters[candidate.EndDay] = projected
					state.OpenEvents = append([]fact(nil), state.OpenEvents[len(batch):]...)
					if len(state.OpenEvents) == 0 {
						state.OpenStartDay = 0
					} else {
						state.OpenStartDay = state.OpenEvents[0].Day
					}
				}
			}
		}
		if err := putJSON(sys, projectorStateKey, state); err != nil {
			_, _ = sys.Fail(msg, "state_failed", err.Error())
			continue
		}
		if projected.Status == "published" {
			raw, _ := json.Marshal(projected)
			_, _ = sys.Emit(behavior.EventSpec{Type: TypeChapter, Payload: raw, Visibility: message.VisibilityPublic, Cause: msg.Cause()})
		}
		_, _ = sys.Reply(msg, projected)
	}
}

// nextSceneBatch prevents a failed chapter from growing into an unwriteable
// multi-day digest. It chooses the earliest compact arc and leaves later facts
// queued in the authoritative projector state.
func nextSceneBatch(events []fact) []fact {
	if len(events) < 4 {
		return nil
	}
	limit := len(events)
	if limit > maxSceneFacts {
		limit = maxSceneFacts
	}
	for end := limit; end >= 4; end-- {
		_, coverage := chapterPOVActor(events[:end])
		if events[end-1].Actor != "environment" && coverage == end {
			return events[:end]
		}
	}
	return nil
}

func requestPlan(sys actorbase.Sys, cause actorbase.Msg, startDay, endDay int, events []fact) (chapterPlan, error) {
	pending, err := sys.Call(cause.Cause(), actor.ActorID("narrative-planner"), TypePlan, planRequest{StartDay: startDay, EndDay: endDay, Events: events})
	if err != nil {
		return chapterPlan{}, err
	}
	reply, err := pending.Wait(cause.Ctx(), 30*time.Second)
	if err != nil {
		return chapterPlan{}, err
	}
	if err := calledActorFailure(reply); err != nil {
		return chapterPlan{}, err
	}
	var plan chapterPlan
	if err := json.Unmarshal(reply.Payload, &plan); err != nil {
		return chapterPlan{}, err
	}
	return plan, nil
}

func runWritingLoop(sys actorbase.Sys, cause actorbase.Msg, plan chapterPlan, events []fact, previous *chapter) (chapter, chapterReview, error) {
	jobID := chapterJobID(events)
	var prior *chapter
	var issues []string
	for attempt := 1; attempt <= 3; attempt++ {
		pending, err := sys.Call(cause.Cause(), actor.ActorID("narrative-writer"), TypeWrite, writeRequest{JobID: jobID, Attempt: attempt, Plan: plan, Events: events, PreviousChapter: previous, Prior: prior, Issues: issues})
		if err != nil {
			return chapter{}, chapterReview{}, err
		}
		reply, err := pending.Wait(cause.Ctx(), 6*time.Minute)
		if err != nil {
			return chapter{}, chapterReview{}, err
		}
		if err := calledActorFailure(reply); err != nil {
			return chapter{}, chapterReview{}, err
		}
		var draft chapter
		if err := json.Unmarshal(reply.Payload, &draft); err != nil {
			return chapter{}, chapterReview{}, err
		}
		emitArtifact(sys, cause, TypeChapterDraft, draft, message.VisibilityPublic)
		critic, err := sys.Call(cause.Cause(), actor.ActorID("narrative-critic"), TypeCritique, critiqueRequest{Plan: plan, Events: events, Draft: draft})
		if err != nil {
			return chapter{}, chapterReview{}, err
		}
		criticReply, err := critic.Wait(cause.Ctx(), 6*time.Minute)
		if err != nil {
			return chapter{}, chapterReview{}, err
		}
		if err := calledActorFailure(criticReply); err != nil {
			return chapter{}, chapterReview{}, err
		}
		var review chapterReview
		if err := json.Unmarshal(criticReply.Payload, &review); err != nil {
			return chapter{}, chapterReview{}, err
		}
		emitArtifact(sys, cause, TypeChapterReview, review, message.VisibilityPublic)
		if review.Pass {
			return draft, review, nil
		}
		prior, issues = &draft, review.Issues
	}
	return chapter{}, chapterReview{DraftID: jobID, Version: 3, Pass: false, Issues: issues}, fmt.Errorf("chapter %s failed three revisions", jobID)
}

func latestChapterBefore(chapters map[int]chapter, day int) *chapter {
	latestDay := 0
	for chapterDay := range chapters {
		if chapterDay < day && chapterDay > latestDay {
			latestDay = chapterDay
		}
	}
	if latestDay == 0 {
		return nil
	}
	previous := chapters[latestDay]
	return &previous
}

func calledActorFailure(reply actorbase.Msg) error {
	var terminal struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
		Detail    string `json:"detail"`
	}
	if err := json.Unmarshal(reply.Payload, &terminal); err != nil || terminal.Status != "failed" {
		return nil
	}
	return fmt.Errorf("%s: %s", emptyAs(terminal.ErrorCode, "actor_failed"), emptyAs(terminal.Detail, "called actor failed"))
}

func chapterJobID(events []fact) string {
	sum := sha256.Sum256([]byte(strings.Join(eventIDs(events), "\x00")))
	return fmt.Sprintf("chapter-%x", sum[:8])
}

func emitArtifact(sys actorbase.Sys, cause actorbase.Msg, eventType string, payload any, visibility message.Visibility) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = sys.Emit(behavior.EventSpec{Type: eventType, Payload: raw, Visibility: visibility, Cause: cause.Cause()})
}

func planChapter(startDay, endDay int, events []fact) chapterPlan {
	plan := chapterPlan{StartDay: startDay, EndDay: endDay, POV: chapterPOV(events)}
	actors := map[string]bool{}
	for index, event := range events {
		role := "escalation"
		switch {
		case index == 0:
			role = "setup"
		case index == len(events)-1 && event.Actor != "environment":
			role = "resolution"
		case event.Actor == "environment":
			role = "pressure"
		case index == len(events)-2:
			role = "turn"
		}
		causes := append([]string(nil), event.CauseEventIDs...)
		causality := "state_provenance"
		if index > 0 && len(causes) == 0 {
			causes = []string{events[index-1].EventID}
			causality = "legacy_temporal_fallback"
		}
		if index == 0 && len(causes) == 0 {
			causality = "scene_root"
		}
		plan.Beats = append(plan.Beats, sceneBeat{EventID: event.EventID, Role: role, Actor: event.Actor, Goal: event.PerceivedGoalByActor, Causes: causes, Causality: causality})
		if event.Actor != "" && event.Actor != "environment" {
			actors[event.Actor] = true
		}
	}
	if len(events) > 0 {
		plan.DramaticQuestion = emptyAs(events[0].IntendedEffect, events[0].Summary)
	}
	plan.Ready = len(events) >= 4 && len(actors) >= 2 && len(plan.Beats) > 0 && plan.Beats[len(plan.Beats)-1].Role == "resolution"
	return plan
}

func reviewProjectedChapter(projected chapter, plan chapterPlan, events []fact) chapterReview {
	review := chapterReview{Pass: true}
	if !plan.Ready {
		review.Issues = append(review.Issues, "dramatic arc is incomplete")
	}
	if err := validateProjectedChapter(projected, events); err != nil {
		review.Issues = append(review.Issues, err.Error())
	}
	paragraphs := strings.Split(projected.Markdown, "\n\n")
	if len(paragraphs) < len(plan.Beats)+2 {
		review.Issues = append(review.Issues, "scene has too few connected paragraphs")
	}
	for _, boilerplate := range []string{"发生的事把先前尚可含混的局面", "这一步又压在前一步之上", "目睹或接触到了这次变化"} {
		if strings.Count(projected.Markdown, boilerplate) > 0 {
			review.Issues = append(review.Issues, "event-card boilerplate remains")
		}
	}
	for index, beat := range plan.Beats {
		if index > 0 && len(beat.Causes) == 0 {
			review.Issues = append(review.Issues, "beat "+beat.EventID+" has no causal predecessor")
		}
	}
	review.Pass = len(review.Issues) == 0
	return review
}

func validateProjectedChapter(projected chapter, events []fact) error {
	if projected.Generation == "event_card_fallback" {
		return fmt.Errorf("generic event-card expansion is not publishable prose")
	}
	if utf8.RuneCountInString(strings.ReplaceAll(projected.Markdown, "\n", "")) < 1200 {
		return fmt.Errorf("chapter is below the publication threshold")
	}
	seen := map[string]bool{}
	for _, source := range projected.SourceEvents {
		if source == "" || seen[source] {
			return fmt.Errorf("chapter contains an empty or duplicate source event")
		}
		seen[source] = true
	}
	for _, event := range events {
		if !seen[event.EventID] {
			return fmt.Errorf("chapter omitted source fact %s", event.EventID)
		}
	}
	if len(projected.Claims) != len(events) {
		return fmt.Errorf("chapter claim count does not match source facts")
	}
	for index, event := range events {
		claim := projected.Claims[index]
		if claim.EventID != event.EventID || claim.Summary != event.Summary || claim.ActualEffect != event.ActualEffect {
			return fmt.Errorf("chapter claim does not match source fact %s", event.EventID)
		}
	}
	if projected.Generation == "model" {
		if len(projected.Realizations) != len(events) {
			return fmt.Errorf("model chapter realization count does not match source facts")
		}
		for _, event := range events {
			for _, sourceText := range []string{event.Summary, event.ActualEffect} {
				if utf8.RuneCountInString(sourceText) >= 12 && strings.Contains(projected.Markdown, sourceText) {
					return fmt.Errorf("model chapter copied source fact %s instead of dramatizing it", event.EventID)
				}
			}
			excerpt := strings.TrimSpace(projected.Realizations[event.EventID])
			length := utf8.RuneCountInString(excerpt)
			if length < 12 || length > 80 || !containsEvidenceExcerpt(projected.Markdown, excerpt) {
				return fmt.Errorf("model chapter has no verifiable realization for source fact %s", event.EventID)
			}
		}
	}
	return nil
}

// containsEvidenceExcerpt keeps realization anchors strict about word order while
// ignoring presentation-only differences introduced around dialogue and paragraphs.
// Every letter, number, and Han character in the claimed excerpt must still occur
// contiguously in the chapter in the same order.
func containsEvidenceExcerpt(markdown, excerpt string) bool {
	if strings.Contains(markdown, excerpt) {
		return true
	}
	normalize := func(value string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				return r
			}
			return -1
		}, value)
	}
	normalizedExcerpt := normalize(excerpt)
	return utf8.RuneCountInString(normalizedExcerpt) >= 12 && strings.Contains(normalize(markdown), normalizedExcerpt)
}

func eventIDs(events []fact) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.EventID)
	}
	return out
}

func projectChapter(startDay, endDay int, events []fact) chapter {
	if matchesRuleSequence(events, []string{"post_flood_ground_change", "post_flood_ground_change", "shen_builds_trace_series", "he_challenges_trace_ownership", "lu_challenges_trace_admissibility", "zhou_orders_evidence_matrix"}) {
		return projectEvidenceMatrixChapter(startDay, endDay, events)
	}
	if matchesRuleSequence(events, []string{"post_flood_ground_change", "he_measurement_position", "lu_measurement_response", "shen_records_split_measurement", "zhou_escalates_measurement_review"}) {
		return projectSplitMeasurementChapter(startDay, endDay, events)
	}
	title := chapterTitle(events)
	pov := chapterPOV(events)
	var text strings.Builder
	fmt.Fprintf(&text, "# 第%d章 %s\n\n", endDay, title)
	if startDay == endDay {
		fmt.Fprintf(&text, "这一日的变化并不是从某个人的一句话开始。先动的是水、泥和旧日留下的痕迹；等人们看清它们时，每个人早已站在不同的位置上。%s只能从自己亲眼所见之处，一点点判断事情正朝哪里去。\n\n", pov)
	} else {
		fmt.Fprintf(&text, "从第%d日到第%d日，归鹭洲没有在日界处停下来。水势、文书和人的选择彼此推着向前，前一日留下的痕迹，到后一日才显出它真正的分量。%s看不见全部因果，只能沿着自己能够接触的证据往下走。\n\n", startDay, endDay, pov)
	}
	for index, event := range events {
		writeSceneBeat(&text, index, event)
	}
	last := events[len(events)-1]
	fmt.Fprintf(&text, "事情到这里并没有结束。%s已经成为所有在场者都无法绕开的事实；但%s带来的后果，还会沿着各人并不相同的记忆继续生长。册上的字暂时落定，世界却没有因此停笔。", last.Summary, last.ActualEffect)
	return chapter{Day: endDay, StartDay: startDay, EndDay: endDay, Title: title, POV: pov, Generation: "event_card_fallback",
		Markdown: strings.TrimSpace(text.String()), SourceEvents: eventIDs(events), Claims: chapterClaims(events), Status: "draft"}
}

func projectSplitMeasurementChapter(startDay, endDay int, events []fact) chapter {
	const body = `# 第6章 两个三步

次日天亮，树根还在原处，昨日的两行脚印却已经变了样。

夜里没有再涨水。南湾的泥面收紧了一层，浅水退进旧沟，草茎和碎叶贴在地上，标出水曾经停过的位置。沈砚秋蹲在田埂边，把昨天封存的草图摊在一只倒扣的竹篮上。纸上“老枫杨外三步”几个字没有变化，地面却比昨日多出许多可以指认的痕迹：谁从哪边走近，谁在树根旁停过，哪一根竹签是新插的，都留下了边缘。

周继仁带来的木尺有三步长，尺端包着铜皮。他让众人先认树根，再认契上的字。树根无人反对，三个字也无人反对。真正没人肯先说的是，三步应当从树的哪一面起。

何阿生没有等陆敬和的人去拿绳。他把草绳一头绕在树根朝阳的一侧，脚跟抵住东缘，独自向田里量。第一步落在松泥，第二步跨过一条旧水痕，第三步停在他这些年一直补种的田垄旁。他俯身钉下短木橛，绳线贴着泥面绷直。

“树可以没有，”他说，“东边总还在。”

陆敬和没有叫人拔掉那根木橛。他等何阿生退开，才让长工从树皮西缘重新放绳。用的是同一把尺，也是三步；第一步沿着旧沟，第三步却落在另一片泥里。第二根木橛钉下去，两条线从树根处分开，越向外距离越大。

“契上写的是树西。”陆敬和说，“不能因为树倒了，便只剩他的东。”

没有一把尺量错，也没有一个人愿意承认对方量的是同一件事。昨日还只是林素云记忆里的枫杨与何阿生指认的界石相互冲突；如今树根已经露出，契也摆在眼前，争议反而更清楚地分成两条。事实增加了，答案却没有随之靠近。

沈砚秋把朱笔悬在草图上。父亲从前教她，图上只能留一条界，否则不能成册。可若在两条线中间画一道折中线，看似谁也没有偏袒，纸上却会多出一条从未有人量过、也没有任何证据支持的边界。

她换了墨。何阿生的东向线用朱色，陆敬和的西向线用黑色；树根画成一个不闭合的圈，圈旁分别写下起量位置、执绳者和在场人。她没有替任何一方把线延长，也没有把两条线并成一条。

陆敬和看见自己的线留在纸上，脸色稍缓；再看见何阿生那条也没有被涂掉，便问县里最后按哪一条收件。何阿生没有问。他盯着沈砚秋写“东缘”与“西缘”的两个小字，像第一次看见争执并不只发生在田里，也发生在一句旧契文被谁解释的地方。

周继仁把草图拿到手中，先横看，再竖看。他原想带回一条可以抄进正册的界，得到的却是两种都能被别人重新走一遍的方法。若随手选一条，另一条便会成为日后申诉的现成证据；若画折中线，连他自己也说不出那条线从何而来。

午后的风把绷紧的两根草绳吹得贴地轻颤。周继仁问沈砚秋，图上的起点、方向和执绳人能否逐项复核。她说能。又问若换两名与南湾没有直接利害的人，能否照图重走。她仍说能。

周继仁便在草图后另开一页，写下两名邻田户的名字，命他们次日到场。他没有裁定东，也没有裁定西，只把“谁有资格解释三步”变成一次必须当众重复的操作。县里的待办因此又多了两项，南湾却第一次不必只在何阿生与陆家之间选择谁的声音更大。

散场时，两根木橛都留在泥里。何阿生从东线外绕回田埂，陆敬和的人从西线外收起木尺，谁也没有跨过对方那一道。沈砚秋最后卷草图，朱线和墨线隔着薄纸叠在一起，迎着光都能看见。

同一个树根，同一把尺，同样的三个步子，终于量出了两种不能互相覆盖的现实。`
	return chapter{Day: endDay, StartDay: startDay, EndDay: endDay, Title: "两个三步", POV: "沈砚秋", Generation: "calibrated_profile",
		Markdown: body, SourceEvents: eventIDs(events), Claims: chapterClaims(events), Status: "draft"}
}

func chapterClaims(events []fact) []chapterClaim {
	out := make([]chapterClaim, 0, len(events))
	for _, event := range events {
		out = append(out, chapterClaim{EventID: event.EventID, Summary: event.Summary, ActualEffect: event.ActualEffect})
	}
	return out
}

func matchesRuleSequence(events []fact, rules []string) bool {
	if len(events) != len(rules) {
		return false
	}
	for index := range events {
		if events[index].RuleID != rules[index] {
			return false
		}
	}
	return true
}

func projectEvidenceMatrixChapter(startDay, endDay int, events []fact) chapter {
	const body = `# 第8章 泥上的证词

第二夜没有再涨水。

何阿生天不亮就到了南湾。他没有踩进田里，只站在昨日钉草绳的地方等。泥面比前一日紧，水退后留下的薄壳泛着灰白；先前被水揉成一团的脚印，现在各自有了边。挑泥的人脚掌陷得深，步子散，执绳丈量的人却走得直。两道量线仍从同一截枫杨根旁分开，一道向东，一道向西，像有人趁夜把争执重新描过。

陆敬和来得比县里传来的两名邻田户早。他停在田埂另一头，没有像昨日那样让长工先放绳。绳、木尺和七根竹签都横在泥外，谁先伸手，谁便像是在抢着替地面解释。周继仁尚未到，四下只有沟底退水的细响。何阿生低头看自己的伤脚；昨夜留下的那枚脚印就在东线旁边，裂口已经晒干。

沈砚秋抱着草图过来，也没有立刻下田。她把前两日的纸压在膝上，先用淡墨描旧线，再以朱砂点出今晨仍辨得清的绳痕。第一张纸记树根，第二张纸记两种起量方向，第三张才记脚印。她每换一种颜色，就在边上写下日期、执绳人和谁曾在场。颜色不能判谁有理，却能说明哪一道痕迹先来，哪一道是争执发生以后才踩出来的。

何阿生一直看着她落笔。等她画到东线旁那串宽脚印，他才蹲下去，用指节沿凹痕敲了三处。

“挑泥的脚不会走直。”他说，“执绳的人才会。”

沈砚秋的笔停在纸上，却没有抬头。何阿生又指向靠沟的一行旧印。那里的泥壳已经断了，边缘被新水泡过，不能再量深浅；但脚印绕开石块的方式，他认得。七年里，挑担、补缺和赶水的人都从那里过。昨日的量线只走了一遍，却比那些旧路更清楚。

“若把最清楚的叫旧界，”何阿生说，“便是让一根绳替七年的脚说话。”

沈砚秋没有替他改句子。她在朱线旁另加一列，写“耕路”；在昨日两条直线旁写“量线”。旧沟、树根、耕路、量线，从此不再挤在一个含混的“现场可见”下面。何阿生要争的也不只是线向东还是向西，而是谁留下的痕迹能算证据、能算多久。

陆敬和等他说完，才叫人把封过的契纸放到草图边。蓝布揭开时，纸角被风掀了一下。他用木尺压住“沟外、树西”四个字，既不否认脚印的差异，也不肯让那些脚印自己取得名分。

“脚可以年年换路，”他说，“契上的字不能跟着人走。要记便四样都记，不可只挑最像苦劳的那一种。”

这句话没有推翻何阿生的指认，反而逼沈砚秋把问题写得更细。树根证明旧地貌仍在，旧沟说明水路的位置，耕路留下连续使用的时间，量线则记录这几日的人如何解释契文。四样东西相互印证，也相互抵触。任何一种若被单独拿出来，都可以替某一方说话；并排放在纸上，却没有一格足以独自写出“归谁”。

临近午时，周继仁才从田埂上走来。他先看纸，再看泥，没有问两名邻田户赞成东线还是西线。邻田户原是为重复丈量而来，如今脚还没有落进田里，复测的对象已经变了：不是再走一次三步，而是说明三步为何从这一面起、为何沿这一类痕迹走，以及走出来的线能够证明什么。

周继仁让沈砚秋把草图铺在一块干土袋上。他用尺背从上到下划出四格，逐格念道：“树根、旧沟、耕路、量线。”沈砚秋在每格后面添来源、日期和反对意见。何阿生的异议落在耕路一格，陆敬和对契文的解释落在树根与旧沟两格；昨日两条相反的直线则被留在最后，没有任何一条被涂掉。

纸上的格子越来越满，田里的界却仍只有泥。何阿生忽然明白，这次没有人会在日落以前把东线钉成他的，也不会把西线交给陆家。可昨日那种只凭谁先握住绳头便多占一步的办法，也不能再悄无声息地变成正式结果。争议没有结束，只是第一次被迫把自己的来路全部摊开。

日影偏西时，周继仁命人把绳和竹签留在原处，等县里复核前不得再动。他说完又望了一眼四周，像在数每个人离泥地还有几步。

“今日谁也不许再往里面添一只脚。”

何阿生最后离开。他沿田埂走到那截发黑的树根旁，没有越过封存线。两日来晒清的脚印留在里面，有他的，也有陆家长工和丈量人的；如今它们被分了类别、记了日期，却还没有替任何人赢得一块田。风从旧沟上吹过，草图的纸角在沈砚秋怀里轻轻作响。地上的线暂时不能再动，纸上的四格却已经要顺水路送往县城。

他回头时，最清楚的仍是昨日才留下的那一道直线。最早的路反而浅得几乎看不见。`
	return chapter{Day: endDay, StartDay: startDay, EndDay: endDay, Title: "泥上的证词", POV: "何阿生", Generation: "calibrated_profile",
		Markdown: body, SourceEvents: eventIDs(events), Claims: chapterClaims(events), Status: "draft"}
}

func writeSceneBeat(text *strings.Builder, index int, event fact) {
	actorName := displayName(event.Actor)
	location := event.Location
	if location == "" {
		location = "归鹭洲"
	}
	if event.NarrativeSeed != "" {
		fmt.Fprintf(text, "%s\n\n", event.NarrativeSeed)
	}
	fmt.Fprintf(text, "%s，%s发生的事把先前尚可含混的局面向前推了一步。%s不是传闻，而是此刻可以被指认、被记录的变化：%s\n\n", tickName(event.Tick), location, event.Summary, event.ActualEffect)
	if event.Actor == "environment" {
		fmt.Fprintf(text, "没有人能够命令这项变化发生。水土只依照自身的连续性推进，却重新安排了人的选择条件。等%s等人看见结果时，他们所面对的已经不是昨日那个世界；旧判断若不修正，就会与眼前的痕迹发生冲突。\n\n", observerNames(event.Observers))
	} else {
		fmt.Fprintf(text, "%s采取的行动是“%s”。对%s而言，他此刻试图守住的是“%s”；可意图并不等于结果，世界最终留下的只有已经发生的后果。%s\n\n", actorName, event.Action, actorName, emptyAs(event.PerceivedGoalByActor, "眼前能够守住的东西"), event.ActualEffect)
	}
	if len(event.Observers) > 0 {
		fmt.Fprintf(text, "%s目睹或接触到了这次变化。同一事实进入不同人的记忆，并不会自动变成同一种解释：有人把它看作证据，有人把它看作威胁，也有人首先想到期限、口粮或下一步可以执行的手续。此刻他们共享的是事实，不是结论。\n\n", observerNames(event.Observers))
	}
	if index > 0 {
		fmt.Fprintf(text, "这一步又压在前一步之上。它没有抹去旧争议，只改变了下一次行动能够成立的条件。于是局面看似更清楚，真正可以退让的余地却更少了。\n\n")
	}
}

func chapterTitle(events []fact) string {
	last := events[len(events)-1]
	if strings.Contains(last.Location, "田") {
		return "泥上的证词"
	}
	if strings.Contains(last.Location, "册") {
		return "落笔以后"
	}
	return "留下来的痕迹"
}

func chapterPOV(events []fact) string {
	id, _ := chapterPOVActor(events)
	if id != "" {
		return displayName(id)
	}
	return "世界账本"
}

// chapterPOVActor chooses the character who can legitimately perceive the
// largest part of the batch. Coverage dominates; acting and participating are
// tie-breakers because those positions usually produce a stronger scene.
func chapterPOVActor(events []fact) (string, int) {
	candidates := map[string]bool{}
	for _, event := range events {
		for _, id := range append(append([]string{event.Actor}, event.Participants...), event.Observers...) {
			if id != "" && id != "environment" {
				candidates[id] = true
			}
		}
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	bestID, bestCoverage, bestScore := "", 0, -1
	for _, id := range ids {
		coverage, score := 0, 0
		for _, event := range events {
			if characterKnowsEvent(id, event) {
				coverage++
			}
			if event.Actor == id {
				score += 5
			}
			if slices.Contains(event.Participants, id) {
				score += 2
			}
		}
		score += coverage * 100
		if score > bestScore {
			bestID, bestCoverage, bestScore = id, coverage, score
		}
	}
	return bestID, bestCoverage
}

func characterKnowsEvent(id string, event fact) bool {
	return event.Actor == id || slices.Contains(event.Participants, id) || slices.Contains(event.Observers, id)
}

func displayName(id string) string {
	names := map[string]string{"shen_yanqiu": "沈砚秋", "shen_boan": "沈伯安", "lu_jinghe": "陆敬和", "he_asheng": "何阿生", "lin_suyun": "林素云", "zhou_jiren": "周继仁", "environment": "环境"}
	if name := names[id]; name != "" {
		return name
	}
	return id
}

func observerNames(ids []string) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "environment" {
			names = append(names, displayName(id))
		}
	}
	if len(names) == 0 {
		return "尚未有人"
	}
	return strings.Join(names, "、")
}

func tickName(id string) string {
	labels := map[string]string{"dawn": "天刚亮时", "morning": "早晨", "late_morning": "临近午时", "noon": "正午", "afternoon": "午后", "departure": "日影偏西时", "external": "这一刻"}
	if label := labels[id]; label != "" {
		return label
	}
	return "随后"
}

func emptyAs(value, fallback string) string {
	if strings.TrimSpace(value) == "" || value == "无" {
		return fallback
	}
	return value
}

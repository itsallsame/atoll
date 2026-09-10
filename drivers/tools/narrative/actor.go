package narrative

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

const (
	TypeEvolve             = "narrative.evolve"
	TypeSchedule           = "narrative.schedule"
	TypeStatus             = "narrative.status"
	TypeInject             = "narrative.inject"
	TypeAutoStart          = "narrative.autorun.start"
	TypeAutoStop           = "narrative.autorun.stop"
	TypeProvenanceBackfill = "narrative.provenance.backfill"
	TypePropose            = "narrative.propose"
	TypeObserve            = "narrative.observe"
	TypeFact               = "narrative.fact"
	TypeDecision           = "narrative.decision"
	TypeTick               = "narrative.tick"
	TypeDayCompleted       = "narrative.day.completed"
	typeTimer              = "narrative.timer"
	typeAutoTimer          = "narrative.autorun.timer"
)

const (
	worldStateKey     resource.ResourceID = "narrative.world"
	characterStateKey resource.ResourceID = "narrative.belief"
)

type evolvePayload struct {
	Day int `json:"day,omitempty"`
}

type schedulePayload struct {
	DelayMS int64 `json:"delay_ms"`
	Day     int   `json:"day,omitempty"`
}

type autoRunPayload struct {
	IntervalMS   int64 `json:"interval_ms"`
	Through      int   `json:"through"`
	MaxEmptyDays int   `json:"max_empty_days,omitempty"`
}

type provenancePayload struct {
	Sources map[string]string `json:"sources"`
}

type tickPayload struct {
	Day  int    `json:"day"`
	Tick string `json:"tick"`
}

type stimulusPayload struct {
	Source       string   `json:"source"`
	Location     string   `json:"location"`
	Actor        string   `json:"actor"`
	Action       string   `json:"action"`
	Summary      string   `json:"summary"`
	ActualEffect string   `json:"actual_effect"`
	Participants []string `json:"participants,omitempty"`
	Observers    []string `json:"observers"`
	Effects      []effect `json:"effects"`
}

type proposalReply struct {
	Character string     `json:"character"`
	Proposals []proposal `json:"proposals"`
}

type evolutionResult struct {
	Day        int            `json:"day"`
	EventCount int            `json:"event_count"`
	Events     []fact         `json:"events"`
	State      map[string]any `json:"state"`
}

func worldDef(spec simulationSpec, initial persistedWorld, channelPrefix string) actorbase.Def {
	return actorbase.Def{Manifest: worldManifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return runWorld(sys, spec, initial, channelPrefix) }, nil
	}}
}

func characterDef(character string, spec simulationSpec, initial belief) actorbase.Def {
	return actorbase.Def{Manifest: characterManifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return runCharacter(sys, character, spec, initial) }, nil
	}}
}

func runWorld(sys actorbase.Sys, spec simulationSpec, initial persistedWorld, channelPrefix string) error {
	world := initial
	out, err := sys.State().Get(worldStateKey)
	if err != nil {
		return fmt.Errorf("narrative-world: read state: %w", err)
	}
	if out.Found {
		if err := json.Unmarshal(out.Value, &world); err != nil {
			return fmt.Errorf("narrative-world: recover state: %w", err)
		}
	}
	if world.State == nil {
		world.State = map[string]any{}
	}
	if world.FiredRules == nil {
		world.FiredRules = map[string]int{}
	}
	if world.RuleCounts == nil {
		world.RuleCounts = map[string]int{}
	}
	if world.StateSources == nil {
		world.StateSources = map[string]string{}
	}
	// A durable timer can become due while the node is still restoring actor
	// endpoints. Re-arm from the persisted boundary on every incarnation so an
	// early no_endpoint_yet delivery cannot leave autorun falsely enabled.
	if world.Auto.Enabled {
		if world.Auto.IntervalMS < 1 {
			world.Auto.IntervalMS = int64(time.Minute / time.Millisecond)
		}
		if world.Auto.TimerID != "" {
			_ = sys.CancelTimer(schedule.TimerID(world.Auto.TimerID))
		}
		timerID, scheduleErr := sys.After(time.Duration(world.Auto.IntervalMS)*time.Millisecond, typeAutoTimer, map[string]any{}, schedule.TimerHomeDurable)
		if scheduleErr != nil {
			world.Auto.Enabled = false
			world.Auto.TimerID = ""
			_ = sys.PublishObs("narrative.autorun.error", []byte(scheduleErr.Error()))
		} else {
			world.Auto.TimerID = string(timerID)
		}
		_ = putJSON(sys, worldStateKey, world)
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			switch msg.Type {
			case typeTimer:
				var payload evolvePayload
				if err := json.Unmarshal(msg.Payload, &payload); err != nil {
					continue
				}
				_, runErr := evolve(sys, msg, spec, &world, channelPrefix, payload.Day)
				if runErr != nil {
					_ = sys.PublishObs("narrative.error", []byte(runErr.Error()))
				}
			case typeAutoTimer:
				runAutoTick(sys, msg, spec, &world, channelPrefix)
			}
			continue
		}

		switch msg.Type {
		case TypeEvolve:
			var payload evolvePayload
			if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil {
				_, _ = sys.Fail(msg, "bad_payload", err.Error())
				continue
			}
			result, err := evolve(sys, msg, spec, &world, channelPrefix, payload.Day)
			if err != nil {
				_, _ = sys.Fail(msg, "evolution_failed", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, result)

		case TypeSchedule:
			var payload schedulePayload
			if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil || payload.DelayMS < 1 {
				_, _ = sys.Fail(msg, "bad_payload", "delay_ms must be positive")
				continue
			}
			timerID, err := sys.After(time.Duration(payload.DelayMS)*time.Millisecond, typeTimer, evolvePayload{Day: payload.Day}, schedule.TimerHomeDurable)
			if err != nil {
				_, _ = sys.Fail(msg, "schedule_failed", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, map[string]any{"timer_id": timerID, "home": "durable"})

		case TypeStatus:
			var empty struct{}
			if err := actorbase.DecodeStrictEmpty(msg.Payload, &empty); err != nil {
				_, _ = sys.Fail(msg, "bad_payload", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, world)

		case TypeInject:
			var payload stimulusPayload
			if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil {
				_, _ = sys.Fail(msg, "bad_payload", err.Error())
				continue
			}
			row, err := injectStimulus(sys, msg, spec, &world, channelPrefix, payload)
			if err != nil {
				_, _ = sys.Fail(msg, "injection_failed", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, row)

		case TypeAutoStart:
			var payload autoRunPayload
			if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil || payload.IntervalMS < 1 || payload.Through <= world.Day {
				_, _ = sys.Fail(msg, "bad_payload", "interval_ms must be positive and through must be after the durable day")
				continue
			}
			if payload.MaxEmptyDays < 1 {
				payload.MaxEmptyDays = 2
			}
			if world.Auto.Enabled {
				_, _ = sys.Fail(msg, "conflict_exists", "autorun is already active")
				continue
			}
			timerID, err := sys.After(time.Duration(payload.IntervalMS)*time.Millisecond, typeAutoTimer, map[string]any{}, schedule.TimerHomeDurable)
			if err != nil {
				_, _ = sys.Fail(msg, "schedule_failed", err.Error())
				continue
			}
			world.Auto = autoRunState{Enabled: true, IntervalMS: payload.IntervalMS, Through: payload.Through, MaxEmptyDays: payload.MaxEmptyDays, TimerID: string(timerID)}
			if err := putJSON(sys, worldStateKey, world); err != nil {
				_, _ = sys.Fail(msg, "state_failed", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, world.Auto)

		case TypeAutoStop:
			var empty struct{}
			if err := actorbase.DecodeStrictEmpty(msg.Payload, &empty); err != nil {
				_, _ = sys.Fail(msg, "bad_payload", err.Error())
				continue
			}
			if world.Auto.TimerID != "" {
				_ = sys.CancelTimer(schedule.TimerID(world.Auto.TimerID))
			}
			world.Auto.Enabled = false
			world.Auto.TimerID = ""
			if err := putJSON(sys, worldStateKey, world); err != nil {
				_, _ = sys.Fail(msg, "state_failed", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, world.Auto)

		case TypeProvenanceBackfill:
			var payload provenancePayload
			if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil {
				_, _ = sys.Fail(msg, "bad_payload", err.Error())
				continue
			}
			added := 0
			for key, source := range payload.Sources {
				if _, exists := world.State[key]; exists && source != "" && world.StateSources[key] == "" {
					world.StateSources[key] = source
					added++
				}
			}
			if err := putJSON(sys, worldStateKey, world); err != nil {
				_, _ = sys.Fail(msg, "state_failed", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, map[string]any{"added": added, "source_count": len(world.StateSources)})

		default:
			_, _ = sys.Fail(msg, "type_unsupported", "narrative world does not handle "+msg.Type)
		}
	}
}

func runAutoTick(sys actorbase.Sys, cause actorbase.Msg, spec simulationSpec, world *persistedWorld, channelPrefix string) {
	if !world.Auto.Enabled {
		return
	}
	result, err := evolve(sys, cause, spec, world, channelPrefix, 0)
	if err != nil {
		world.Auto.Enabled = false
		world.Auto.TimerID = ""
		_ = putJSON(sys, worldStateKey, world)
		_ = sys.PublishObs("narrative.autorun.error", []byte(err.Error()))
		return
	}
	if result.EventCount == 0 {
		world.Auto.EmptyDays++
	} else {
		world.Auto.EmptyDays = 0
	}
	if world.Day >= world.Auto.Through || world.Auto.EmptyDays >= world.Auto.MaxEmptyDays {
		world.Auto.Enabled = false
		world.Auto.TimerID = ""
		_ = putJSON(sys, worldStateKey, world)
		return
	}
	timerID, err := sys.After(time.Duration(world.Auto.IntervalMS)*time.Millisecond, typeAutoTimer, map[string]any{}, schedule.TimerHomeDurable)
	if err != nil {
		world.Auto.Enabled = false
		world.Auto.TimerID = ""
		_ = sys.PublishObs("narrative.autorun.error", []byte(err.Error()))
	} else {
		world.Auto.TimerID = string(timerID)
	}
	_ = putJSON(sys, worldStateKey, world)
}

func injectStimulus(sys actorbase.Sys, cause actorbase.Msg, spec simulationSpec, world *persistedWorld, channelPrefix string, payload stimulusPayload) (fact, error) {
	payload.Source = strings.TrimSpace(payload.Source)
	payload.Summary = strings.TrimSpace(payload.Summary)
	if payload.Source == "" || payload.Summary == "" || len(payload.Effects) == 0 {
		return fact{}, fmt.Errorf("source, summary and effects are required")
	}
	for _, effect := range payload.Effects {
		if effect.Key == "" || !validEffectOp(effect.Op) {
			return fact{}, fmt.Errorf("unsupported effect %q for %q", effect.Op, effect.Key)
		}
	}
	knownCharacters := map[string]bool{}
	for _, character := range spec.Characters {
		knownCharacters[character] = true
	}
	seenObservers := map[string]bool{}
	for _, observer := range payload.Observers {
		if !knownCharacters[observer] || seenObservers[observer] {
			return fact{}, fmt.Errorf("observer %q is absent from cast or duplicated", observer)
		}
		seenObservers[observer] = true
	}
	world.Seq++
	actorName := strings.TrimSpace(payload.Actor)
	if actorName == "" {
		actorName = "external:" + payload.Source
	}
	row := fact{
		EventID: fmt.Sprintf("D%02d-E%03d", world.Day, world.Seq), Day: world.Day, Tick: "external",
		RuleID: "external/" + payload.Source, Location: payload.Location, Actor: actorName,
		Participants: payload.Participants, Observers: payload.Observers, Action: payload.Action,
		Summary: payload.Summary, ActualEffect: payload.ActualEffect,
		WorldStateDelta: applyEffects(world.State, payload.Effects), Origin: "external",
	}
	for _, effect := range payload.Effects {
		if effect.Op == "delete" {
			delete(world.StateSources, effect.Key)
		} else {
			world.StateSources[effect.Key] = row.EventID
		}
	}
	if err := emitFact(sys, cause, channelPrefix, row); err != nil {
		return fact{}, err
	}
	if err := putJSON(sys, worldStateKey, world); err != nil {
		return fact{}, err
	}
	return row, nil
}

func evolve(sys actorbase.Sys, cause actorbase.Msg, spec simulationSpec, world *persistedWorld, channelPrefix string, requestedDay int) (evolutionResult, error) {
	day := requestedDay
	if day == 0 {
		day = world.Day + 1
	}
	if day != world.Day+1 {
		return evolutionResult{}, fmt.Errorf("day must be the next durable day: got %d, want %d", day, world.Day+1)
	}
	characters := spec.Characters
	if len(characters) == 0 {
		characters = characterIDs(spec.Policies)
	}
	events := make([]fact, 0)
	for _, tick := range spec.Ticks {
		systemPending, err := sys.Call(cause.Cause(), actor.ActorID("narrative-environment"), TypeSystemPropose, systemTickPayload{
			Day: day, Tick: tick.ID, State: cloneMap(world.State), FiredRules: cloneInts(world.FiredRules), RuleCounts: cloneInts(world.RuleCounts),
		})
		if err != nil {
			return evolutionResult{}, fmt.Errorf("call environment: %w", err)
		}
		systemResponse, err := systemPending.Wait(cause.Ctx(), 30*time.Second)
		if err != nil {
			return evolutionResult{}, fmt.Errorf("wait environment: %w", err)
		}
		var systemReply systemProposalReply
		if err := json.Unmarshal(systemResponse.Payload, &systemReply); err != nil {
			return evolutionResult{}, fmt.Errorf("decode environment proposals: %w", err)
		}
		for _, proposed := range systemReply.Proposals {
			rule, selected, err := validateProposal(spec.Processes, proposed, "environment", tick.ID)
			if err != nil {
				emitDecision(sys, cause, proposalDecision(day, proposed, "rejected", err.Error(), "endogenous"))
				continue
			}
			if !canFire(rule, world.FiredRules[rule.ID], world.RuleCounts[rule.ID], day) || !conditionsMatch(world.State, rule.When) {
				emitDecision(sys, cause, proposalDecision(day, proposed, "rejected", "world preconditions no longer hold", "endogenous"))
				continue
			}
			emitDecision(sys, cause, proposalDecision(day, proposed, "accepted", "world preconditions hold", "endogenous"))
			row := settle(world, day, tick.ID, rule, selected)
			if err := emitFact(sys, cause, channelPrefix, row); err != nil {
				return evolutionResult{}, err
			}
			events = append(events, row)
		}
		for _, character := range characters {
			pending, err := sys.Call(cause.Cause(), peerTarget(channelPrefix, character), TypePropose, tickPayload{Day: day, Tick: tick.ID})
			if err != nil {
				return evolutionResult{}, fmt.Errorf("call character %s: %w", character, err)
			}
			response, err := pending.Wait(cause.Ctx(), 30*time.Second)
			if err != nil {
				return evolutionResult{}, fmt.Errorf("wait character %s: %w", character, err)
			}
			var reply proposalReply
			if err := json.Unmarshal(response.Payload, &reply); err != nil {
				return evolutionResult{}, fmt.Errorf("decode character %s: %w", character, err)
			}
			for _, proposed := range reply.Proposals {
				rule, selected, err := validateProposal(spec.Policies, proposed, character, tick.ID)
				if err != nil {
					emitDecision(sys, cause, proposalDecision(day, proposed, "rejected", err.Error(), "endogenous"))
					continue
				}
				if !canFire(rule, world.FiredRules[rule.ID], world.RuleCounts[rule.ID], day) || !conditionsMatch(world.State, rule.When) {
					emitDecision(sys, cause, proposalDecision(day, proposed, "rejected", "world preconditions no longer hold", "endogenous"))
					continue
				}
				emitDecision(sys, cause, proposalDecision(day, proposed, "accepted", "world preconditions hold", "endogenous"))
				row := settle(world, day, tick.ID, rule, selected)
				if err := emitFact(sys, cause, channelPrefix, row); err != nil {
					return evolutionResult{}, err
				}
				events = append(events, row)
			}
		}
	}
	world.Day = day
	if err := putJSON(sys, worldStateKey, world); err != nil {
		return evolutionResult{}, err
	}
	result := evolutionResult{Day: day, EventCount: len(events), Events: events, State: cloneMap(world.State)}
	if pending, callErr := sys.Call(cause.Cause(), actor.ActorID("narrative-projector"), TypeProject, result); callErr == nil {
		if _, waitErr := pending.Wait(cause.Ctx(), 36*time.Minute); waitErr != nil {
			_ = sys.PublishObs("narrative.projection.error", []byte(waitErr.Error()))
		}
	} else {
		_ = sys.PublishObs("narrative.projection.error", []byte(callErr.Error()))
	}
	raw, _ := json.Marshal(result)
	_, _ = sys.Emit(behavior.EventSpec{Type: TypeDayCompleted, Payload: raw, Visibility: message.VisibilityPublic, Cause: cause.Cause()})
	return result, nil
}

func proposalDecision(day int, proposed proposal, status, reason, origin string) decision {
	return decision{Day: day, Tick: proposed.Tick, RuleID: proposed.RuleID, Actor: proposed.Actor,
		SelectedOption: proposed.SelectedOption, Action: proposed.Action, Goal: proposed.Goal,
		ExpectedEffect: proposed.ExpectedEffect, Status: status, Reason: reason, Origin: origin}
}

func emitDecision(sys actorbase.Sys, cause actorbase.Msg, row decision) {
	raw, err := json.Marshal(row)
	if err != nil {
		return
	}
	_, _ = sys.Emit(behavior.EventSpec{Type: TypeDecision, Payload: raw, Visibility: message.VisibilityPublic, Cause: cause.Cause()})
}

func settle(world *persistedWorld, day int, tickID string, rule rule, selected option) fact {
	world.Seq++
	world.FiredRules[rule.ID] = day
	world.RuleCounts[rule.ID]++
	selectedScore := optionScore(selected, world.State)
	eventID := fmt.Sprintf("D%02d-E%03d", day, world.Seq)
	causes := causalSources(world.StateSources, rule.When, selected.Modifiers)
	delta := applyEffects(world.State, selected.Effects)
	row := fact{
		EventID: eventID, Day: day, Tick: tickID,
		RuleID: rule.ID, Location: rule.Location, Actor: rule.Actor,
		Participants: rule.Participants, Observers: rule.Observers,
		Action: selected.Action, Summary: selected.Summary,
		PerceivedGoalByActor: selected.PerceivedGoal, IntendedEffect: selected.IntendedEffect,
		ActualEffect: selected.ActualEffect, WorldStateDelta: delta,
		SelectedOption: selected.ID, SelectedScore: selectedScore, Origin: "endogenous", NarrativeSeed: selected.Prose, CauseEventIDs: causes,
	}
	for _, effect := range selected.Effects {
		if effect.Op == "delete" {
			delete(world.StateSources, effect.Key)
		} else {
			world.StateSources[effect.Key] = eventID
		}
	}
	return row
}

func causalSources(sources map[string]string, conditions []condition, modifiers []modifier) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(rows []condition) {
		for _, row := range rows {
			if source := sources[row.Key]; source != "" && !seen[source] {
				seen[source] = true
				out = append(out, source)
			}
		}
	}
	add(conditions)
	for _, modifier := range modifiers {
		add(modifier.When)
	}
	sort.Strings(out)
	return out
}

func emitFact(sys actorbase.Sys, cause actorbase.Msg, channelPrefix string, row fact) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	eventID, err := sys.Emit(behavior.EventSpec{Type: TypeFact, Payload: raw, Visibility: message.VisibilityPublic, Cause: cause.Cause()})
	if err != nil {
		return err
	}
	deliveryCause := message.Anchored(eventID, cause.CorrelationID)
	for _, observer := range row.Observers {
		if observer == "environment" {
			continue
		}
		pending, err := sys.Call(deliveryCause, peerTarget(channelPrefix, observer), TypeObserve, row)
		if err != nil {
			return fmt.Errorf("deliver fact %s to %s: %w", row.EventID, observer, err)
		}
		if _, err := pending.Wait(cause.Ctx(), 30*time.Second); err != nil {
			return fmt.Errorf("wait fact %s observer %s: %w", row.EventID, observer, err)
		}
	}
	return nil
}

func runCharacter(sys actorbase.Sys, character string, spec simulationSpec, initial belief) error {
	private := initial
	out, err := sys.State().Get(characterStateKey)
	if err != nil {
		return fmt.Errorf("narrative-character: read state: %w", err)
	}
	if out.Found {
		if err := json.Unmarshal(out.Value, &private); err != nil {
			return fmt.Errorf("narrative-character: recover state: %w", err)
		}
	}
	if private.Facts == nil {
		private.Facts = map[string]any{}
	}
	if private.Sources == nil {
		private.Sources = map[string]string{}
	}
	if private.UsedRules == nil {
		private.UsedRules = map[string]int{}
	}
	if private.RuleCounts == nil {
		private.RuleCounts = map[string]int{}
	}
	if !out.Found {
		if err := putJSON(sys, characterStateKey, private); err != nil {
			return fmt.Errorf("narrative-character: initialize state: %w", err)
		}
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			continue
		}
		switch msg.Type {
		case TypeObserve:
			var row fact
			if err := actorbase.DecodeStrict(msg.Payload, &row); err != nil || row.EventID == "" {
				_, _ = sys.Fail(msg, "bad_payload", "a complete narrative fact is required")
				continue
			}
			private.ObservedEventIDs = append(private.ObservedEventIDs, row.EventID)
			if row.Actor == character {
				private.UsedRules[row.RuleID] = row.Day
				private.RuleCounts[row.RuleID]++
			}
			for key, value := range row.WorldStateDelta {
				private.Facts[key] = value
				private.Sources[key] = row.EventID + " · " + row.Summary
			}
			if err := putJSON(sys, characterStateKey, private); err != nil {
				_, _ = sys.Fail(msg, "state_failed", err.Error())
				continue
			}
			_, _ = sys.Reply(msg, map[string]any{"observed": row.EventID, "character": character})
		case TypePropose:
			var payload tickPayload
			if err := actorbase.DecodeStrict(msg.Payload, &payload); err != nil || payload.Day < 1 || payload.Tick == "" {
				_, _ = sys.Fail(msg, "bad_payload", "day and tick are required")
				continue
			}
			proposals := proposalsFor(character, payload.Day, payload.Tick, spec.Policies, private.Facts, private.UsedRules, private.RuleCounts)
			_, _ = sys.Reply(msg, proposalReply{Character: character, Proposals: proposals})
		default:
			_, _ = sys.Fail(msg, "type_unsupported", "narrative character does not handle "+msg.Type)
		}
	}
}

func proposalsFor(character string, day int, tickID string, rules []rule, facts map[string]any, used, counts map[string]int) []proposal {
	out := make([]proposal, 0)
	for _, rule := range rulesForTick(rules, tickID) {
		if rule.Actor != character || !canFire(rule, used[rule.ID], counts[rule.ID], day) || !conditionsMatch(facts, rule.When) {
			continue
		}
		selected, err := choose(rule.Options, facts)
		if err != nil {
			continue
		}
		known := map[string]any{}
		for _, condition := range rule.When {
			known[condition.Key] = facts[condition.Key]
		}
		out = append(out, proposal{RuleID: rule.ID, Actor: character, Tick: tickID, SelectedOption: selected.ID,
			Action: selected.Action, Goal: selected.PerceivedGoal, ExpectedEffect: selected.IntendedEffect,
			Preconditions: append([]condition(nil), rule.When...), KnownFacts: known})
	}
	return out
}

func validateProposal(rules []rule, proposed proposal, character, tickID string) (rule, option, error) {
	for _, candidate := range rules {
		if candidate.ID != proposed.RuleID {
			continue
		}
		if candidate.Actor != character || proposed.Actor != character || candidate.Tick != tickID || proposed.Tick != tickID {
			return rule{}, option{}, fmt.Errorf("proposal %s has invalid actor or tick", proposed.RuleID)
		}
		for _, selected := range candidate.Options {
			if selected.ID == proposed.SelectedOption {
				if proposed.Action != selected.Action || proposed.Goal != selected.PerceivedGoal || proposed.ExpectedEffect != selected.IntendedEffect {
					return rule{}, option{}, fmt.Errorf("proposal %s action intent does not match its selected option", proposed.RuleID)
				}
				return candidate, selected, nil
			}
		}
		return rule{}, option{}, fmt.Errorf("proposal %s selected unknown option %s", proposed.RuleID, proposed.SelectedOption)
	}
	return rule{}, option{}, fmt.Errorf("proposal references unknown rule %s", proposed.RuleID)
}

func characterIDs(rules []rule) []string {
	seen := map[string]bool{}
	for _, rule := range rules {
		if rule.Actor != "" && rule.Actor != "environment" {
			seen[rule.Actor] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func putJSON(sys actorbase.Sys, key resource.ResourceID, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	out, err := sys.State().Put(key, raw)
	if err != nil {
		return err
	}
	if !out.Accepted() {
		return fmt.Errorf("state write rejected: %s", out.RejectReason)
	}
	return nil
}

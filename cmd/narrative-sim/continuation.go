package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type continuationSpec struct {
	Ticks     []continuationTick `yaml:"ticks"`
	Processes []continuationRule `yaml:"environment_processes"`
	Policies  []continuationRule `yaml:"actor_policies"`
	Chapters  []generatedChapter `yaml:"chapters"`
	Chapter   generatedChapter   `yaml:"chapter"`
}

type continuationTick struct {
	ID    string `yaml:"id"`
	Label string `yaml:"label"`
}

type continuationRule struct {
	ID           string               `yaml:"id"`
	Tick         string               `yaml:"tick"`
	Order        int                  `yaml:"order"`
	Actor        string               `yaml:"actor"`
	Location     string               `yaml:"location"`
	When         []stateCondition     `yaml:"when"`
	Options      []continuationOption `yaml:"options"`
	Participants []string             `yaml:"participants"`
	Observers    []string             `yaml:"observers"`
}

type stateCondition struct {
	Key   string `yaml:"key"`
	Op    string `yaml:"op"`
	Value any    `yaml:"value"`
}

type continuationOption struct {
	ID             string         `yaml:"id"`
	Action         string         `yaml:"action"`
	Summary        string         `yaml:"summary"`
	PerceivedGoal  string         `yaml:"perceived_goal"`
	IntendedEffect string         `yaml:"intended_effect"`
	ActualEffect   string         `yaml:"actual_effect"`
	Effects        []stateEffect  `yaml:"effects"`
	Utility        map[string]int `yaml:"utility"`
	Prose          string         `yaml:"prose"`
}

type stateEffect struct {
	Key   string `yaml:"key"`
	Op    string `yaml:"op"`
	Value any    `yaml:"value"`
}

type generatedChapter struct {
	Title   string           `yaml:"title"`
	POV     string           `yaml:"pov"`
	Opening string           `yaml:"opening"`
	Closing string           `yaml:"closing"`
	When    []stateCondition `yaml:"when"`
}

type beliefLedger struct {
	Day        int                        `json:"day"`
	Characters map[string]characterBelief `json:"characters"`
}

type characterBelief struct {
	Facts             map[string]any    `json:"facts"`
	Sources           map[string]string `json:"sources"`
	ObservedEventIDs  []string          `json:"observed_event_ids"`
	UncertaintyNotice string            `json:"uncertainty_notice"`
}

type actionProposal struct {
	EventID      string              `json:"event_id"`
	RuleID       string              `json:"rule_id"`
	Actor        string              `json:"actor"`
	Tick         string              `json:"tick"`
	KnownFacts   map[string]any      `json:"known_facts"`
	Candidates   []proposalCandidate `json:"candidates"`
	Selected     string              `json:"selected"`
	SelectionWhy string              `json:"selection_why"`
}

type proposalCandidate struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Score  int    `json:"score"`
}

func continueRuns(bundleDir, outDir string, count int) error {
	if count < 1 {
		return errors.New("continue count must be positive")
	}
	var spec continuationSpec
	if err := loadYAML(filepath.Join(bundleDir, "simulation.yaml"), &spec); err != nil {
		return err
	}
	if len(spec.Ticks) == 0 || len(spec.Policies) == 0 {
		return errors.New("simulation requires ticks and actor policies")
	}
	indexPath := filepath.Join(outDir, "index.json")
	var index []runIndexEntry
	if err := loadJSON(indexPath, &index); err != nil {
		return fmt.Errorf("load run index: %w", err)
	}
	if len(index) == 0 {
		return errors.New("run index is empty; run --all first")
	}
	latest := index[len(index)-1]
	var previous stateDiff
	if err := loadJSON(filepath.Join(outDir, latest.Directory, "state-diff.json"), &previous); err != nil {
		return err
	}
	state := cloneMap(previous.After)
	beliefs, err := replayBeliefs(bundleDir, outDir, index)
	if err != nil {
		return err
	}
	for n := 0; n < count; n++ {
		day := latest.Day + n + 1
		result, err := runContinuationDay(spec, day, state, beliefs)
		if err != nil {
			return err
		}
		directory := fmt.Sprintf("day-%02d", day)
		if err := writeResult(filepath.Join(outDir, directory), result); err != nil {
			return err
		}
		state = result.StateDiff.After
		beliefs = cloneBeliefs(result.Beliefs.Characters)
		entry := runIndexEntry{Day: day, ScenarioID: result.Summary.ScenarioID, Title: result.Summary.Title, ChapterTitle: result.Summary.ChapterTitle, POV: result.Summary.POV, Directory: directory, EventCount: len(result.Events), Excerpt: chapterExcerpt(result.Chapter)}
		index = append(index, entry)
	}
	if err := writeJSONFile(indexPath, index); err != nil {
		return err
	}
	fmt.Printf("continued narrative through day %d\n", index[len(index)-1].Day)
	return nil
}

func runContinuationDay(spec continuationSpec, day int, inherited map[string]any, inheritedBeliefs map[string]characterBelief) (runResult, error) {
	state := cloneMap(inherited)
	beliefs := cloneBeliefs(inheritedBeliefs)
	chapter, err := chapterForState(spec, inherited)
	if err != nil {
		return runResult{}, err
	}
	events := []event{}
	proposals := []actionProposal{}
	prose := []string{chapter.Opening}
	for _, tick := range spec.Ticks {
		rules := rulesForTick(spec, tick.ID)
		for _, rule := range rules {
			decisionState := state
			if rule.Actor != "environment" {
				decisionState = ensureBelief(beliefs, rule.Actor).Facts
			}
			if !conditionsMatch(decisionState, rule.When) {
				continue
			}
			selected, rejected, err := chooseContinuation(rule.Options, decisionState)
			if err != nil {
				return runResult{}, fmt.Errorf("rule %s: %w", rule.ID, err)
			}
			delta := applyEffects(state, selected.Effects)
			id := fmt.Sprintf("E%03d", len(events)+1)
			causes := []string{}
			if len(events) > 0 {
				causes = []string{events[len(events)-1].EventID}
			}
			basis := conditionKeys(rule.When)
			occurredAt := tickLabel(day, tick.Label)
			e := event{EventID: id, SourceID: "generated/" + rule.ID, OccurredAt: occurredAt, Location: rule.Location, Actor: rule.Actor, Participants: rule.Participants, Action: selected.Action, Summary: selected.Summary, PerceivedGoalByActor: selected.PerceivedGoal, CauseEventIDs: causes, IntendedEffect: selected.IntendedEffect, ActualEffect: selected.ActualEffect, WorldStateDelta: delta, Observers: rule.Observers, SelectedOption: selected.ID, SelectedScore: utilityScore(selected.Utility), Utility: selected.Utility, RejectedOptions: rejected, DecisionBasis: basis}
			events = append(events, e)
			if rule.Actor != "environment" {
				proposals = append(proposals, proposalRecord(e, rule, decisionState))
			}
			observeEvent(beliefs, e)
			if selected.Prose != "" {
				prose = append(prose, selected.Prose)
			}
		}
	}
	if len(events) == 0 {
		return runResult{}, errors.New("continuation produced no events")
	}
	prose = append(prose, chapter.Closing)
	chapterText := fmt.Sprintf("# 第%d章 %s\n\n%s\n", day, chapter.Title, strings.Join(nonEmpty(prose), "\n\n"))
	diff := stateDiff{Scenario: fmt.Sprintf("generated-day-%02d", day), Before: cloneMap(inherited), After: cloneMap(state)}
	for _, e := range events {
		diff.Changes = append(diff.Changes, map[string]string{"event_id": e.EventID, "change": e.ActualEffect})
	}
	summary := runSummary{ScenarioID: diff.Scenario, Day: day, Title: chapter.Title, ChapterTitle: chapter.Title, POV: chapter.POV, EventCount: len(events), EmergentEvents: len(events), EndConditionMet: true, RegistrationState: "continued"}
	b := bundle{Title: "名册之外", Scenario: scenario{Day: day, ID: diff.Scenario, Title: chapter.Title, EndCondition: "本日所有可触发过程与人物行动完成", Inherits: fmt.Sprintf("day-%02d", day-1), Chapter: chapterSpec{Title: chapter.Title, POV: chapter.POV}}}
	return runResult{Events: events, Memories: deriveMemories(events, uniqueObservers(events)), Beliefs: beliefLedger{Day: day, Characters: beliefs}, Proposals: proposals, StateDiff: diff, Scene: sceneMaterial(b, events), Chapter: chapterText, Summary: summary}, nil
}

func chapterForState(spec continuationSpec, state map[string]any) (generatedChapter, error) {
	for _, chapter := range spec.Chapters {
		if conditionsMatch(state, chapter.When) {
			return chapter, nil
		}
	}
	if spec.Chapter.Title != "" {
		return spec.Chapter, nil
	}
	return generatedChapter{}, errors.New("no chapter profile matches the current state")
}

func tickLabel(day int, label string) string {
	phase := label
	if index := strings.LastIndex(label, " "); index >= 0 {
		phase = label[index+1:]
	}
	dates := map[int]string{1: "同治六年五月初八", 2: "同治六年五月初九", 3: "同治六年五月初十", 4: "同治六年五月十一", 5: "同治六年五月十二"}
	date, ok := dates[day]
	if !ok {
		date = fmt.Sprintf("演化第%d日", day)
	}
	return date + " " + phase
}

func replayBeliefs(bundleDir, outDir string, index []runIndexEntry) (map[string]characterBelief, error) {
	var cast castFile
	if err := loadYAML(filepath.Join(bundleDir, "cast.yaml"), &cast); err != nil {
		return nil, err
	}
	beliefs := map[string]characterBelief{}
	for _, character := range cast.Characters {
		beliefs[character.ID] = newBelief()
	}
	for _, entry := range index {
		path := filepath.Join(outDir, entry.Directory, "ledger.jsonl")
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var e event
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
			observeEventAs(beliefs, e, entry.Directory+"/"+e.EventID)
		}
		if err := scanner.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	return beliefs, nil
}

func observeEvent(beliefs map[string]characterBelief, e event) {
	observeEventAs(beliefs, e, e.EventID)
}

func observeEventAs(beliefs map[string]characterBelief, e event, eventRef string) {
	for _, observer := range e.Observers {
		if observer == "environment" {
			continue
		}
		belief := ensureBelief(beliefs, observer)
		belief.ObservedEventIDs = append(belief.ObservedEventIDs, eventRef)
		for key, value := range e.WorldStateDelta {
			if key == "director_source" {
				continue
			}
			belief.Facts[key] = value
			belief.Sources[key] = eventRef + " · " + e.Summary
		}
		beliefs[observer] = belief
	}
}

func ensureBelief(beliefs map[string]characterBelief, actor string) characterBelief {
	belief, ok := beliefs[actor]
	if !ok || belief.Facts == nil {
		belief = newBelief()
		beliefs[actor] = belief
	}
	return belief
}

func newBelief() characterBelief {
	return characterBelief{Facts: map[string]any{}, Sources: map[string]string{}, UncertaintyNotice: "未观察到的事实不存在于此人的决策输入中。"}
}

func cloneBeliefs(source map[string]characterBelief) map[string]characterBelief {
	result := make(map[string]characterBelief, len(source))
	for id, belief := range source {
		facts := cloneMap(belief.Facts)
		sources := make(map[string]string, len(belief.Sources))
		for key, value := range belief.Sources {
			sources[key] = value
		}
		result[id] = characterBelief{Facts: facts, Sources: sources, ObservedEventIDs: append([]string(nil), belief.ObservedEventIDs...), UncertaintyNotice: belief.UncertaintyNotice}
	}
	return result
}

func conditionKeys(conditions []stateCondition) []string {
	keys := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		keys = append(keys, condition.Key)
	}
	return keys
}

func proposalRecord(e event, rule continuationRule, beliefs map[string]any) actionProposal {
	known := map[string]any{}
	for _, condition := range rule.When {
		known[condition.Key] = beliefs[condition.Key]
	}
	candidates := make([]proposalCandidate, 0, len(rule.Options))
	for _, option := range rule.Options {
		candidates = append(candidates, proposalCandidate{ID: option.ID, Action: option.Action, Score: utilityScore(option.Utility)})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	return actionProposal{EventID: e.EventID, RuleID: rule.ID, Actor: rule.Actor, Tick: e.OccurredAt, KnownFacts: known, Candidates: candidates, Selected: e.SelectedOption, SelectionWhy: fmt.Sprintf("只依据 %d 条已知事实，选择总效用 %d 的行动。", len(known), e.SelectedScore)}
}

func rulesForTick(spec continuationSpec, tick string) []continuationRule {
	var rules []continuationRule
	for _, rule := range append(append([]continuationRule{}, spec.Processes...), spec.Policies...) {
		if rule.Tick == tick {
			rules = append(rules, rule)
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Order < rules[j].Order })
	return rules
}

func conditionsMatch(state map[string]any, conditions []stateCondition) bool {
	for _, condition := range conditions {
		actual, exists := state[condition.Key]
		switch condition.Op {
		case "exists":
			if !exists {
				return false
			}
		case "eq":
			if !exists || fmt.Sprint(actual) != fmt.Sprint(condition.Value) {
				return false
			}
		case "gte":
			if !exists || number(actual) < number(condition.Value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func chooseContinuation(options []continuationOption, _ map[string]any) (continuationOption, []rejectedOption, error) {
	if len(options) == 0 {
		return continuationOption{}, nil, errors.New("no options")
	}
	sorted := append([]continuationOption(nil), options...)
	sort.Slice(sorted, func(i, j int) bool {
		si, sj := utilityScore(sorted[i].Utility), utilityScore(sorted[j].Utility)
		if si == sj {
			return sorted[i].ID < sorted[j].ID
		}
		return si > sj
	})
	rejected := make([]rejectedOption, 0, len(sorted)-1)
	for _, option := range sorted[1:] {
		rejected = append(rejected, rejectedOption{OptionID: option.ID, Score: utilityScore(option.Utility)})
	}
	return sorted[0], rejected, nil
}

func applyEffects(state map[string]any, effects []stateEffect) map[string]any {
	delta := map[string]any{}
	for _, effect := range effects {
		var next any
		switch effect.Op {
		case "add":
			next = number(state[effect.Key]) + number(effect.Value)
		default:
			next = effect.Value
		}
		state[effect.Key] = next
		delta[effect.Key] = next
	}
	return delta
}

func number(value any) float64 {
	switch value := value.(type) {
	case int:
		return float64(value)
	case int8:
		return float64(value)
	case int16:
		return float64(value)
	case int32:
		return float64(value)
	case int64:
		return float64(value)
	case uint:
		return float64(value)
	case uint8:
		return float64(value)
	case uint16:
		return float64(value)
	case uint32:
		return float64(value)
	case uint64:
		return float64(value)
	case float32:
		return float64(value)
	case float64:
		return value
	case json.Number:
		result, _ := value.Float64()
		return result
	default:
		return 0
	}
}

func uniqueObservers(events []event) []string {
	seen := map[string]bool{}
	var result []string
	for _, e := range events {
		for _, observer := range e.Observers {
			if observer != "environment" && !seen[observer] {
				seen[observer] = true
				result = append(result, observer)
			}
		}
	}
	return result
}

func nonEmpty(values []string) []string {
	var result []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func loadJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

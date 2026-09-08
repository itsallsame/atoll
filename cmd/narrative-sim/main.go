// Command narrative-sim runs the deterministic first experiment in a
// narrative bundle. It deliberately stops at auditable scene material: prose
// generation is a downstream editorial operation, never part of world truth.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

type bundle struct {
	Title      string
	Characters map[string]string
	Scenario   scenario
}

type constitutionFile struct {
	WorkTitle string `yaml:"work_title"`
}

type castFile struct {
	Characters []struct {
		ID   string `yaml:"id"`
		Name string `yaml:"name"`
	} `yaml:"characters"`
}

type scenario struct {
	ID                 string                            `yaml:"scenario_id"`
	Day                int                               `yaml:"day"`
	Title              string                            `yaml:"title"`
	Start              string                            `yaml:"start"`
	EndCondition       string                            `yaml:"end_condition"`
	Inherits           string                            `yaml:"inherits"`
	ResultStateKey     string                            `yaml:"result_state_key"`
	InitialWorldState  map[string]any                    `yaml:"initial_world_state"`
	MemoryCharacters   []string                          `yaml:"memory_characters"`
	Chapter            chapterSpec                       `yaml:"chapter"`
	InitialPublicState map[string]any                    `yaml:"initial_public_state"`
	InitialPrivate     map[string]map[string]any         `yaml:"initial_private_state"`
	SeedEvents         []seedEvent                       `yaml:"seed_events"`
	DecisionPoints     []decisionPoint                   `yaml:"decision_points"`
	RequiredOutputs    map[string]map[string]interface{} `yaml:"required_outputs"`
}

type chapterSpec struct {
	Number   int              `yaml:"number"`
	Title    string           `yaml:"title"`
	POV      string           `yaml:"pov"`
	Opening  string           `yaml:"opening"`
	Passages []chapterPassage `yaml:"passages"`
	Closing  string           `yaml:"closing"`
}

type chapterPassage struct {
	Source string `yaml:"source"`
	Option string `yaml:"option"`
	Text   string `yaml:"text"`
}

type seedEvent struct {
	ID             string   `yaml:"id"`
	Order          int      `yaml:"order"`
	At             string   `yaml:"at"`
	Event          string   `yaml:"event"`
	DirectorSource string   `yaml:"director_source"`
	Location       string   `yaml:"location"`
	Actor          string   `yaml:"actor"`
	Action         string   `yaml:"action"`
	PerceivedGoal  string   `yaml:"perceived_goal"`
	IntendedEffect string   `yaml:"intended_effect"`
	ActualEffect   string   `yaml:"actual_effect"`
	Participants   []string `yaml:"participants"`
	Observers      []string `yaml:"observers"`
}

type decisionPoint struct {
	ID           string           `yaml:"id"`
	Order        int              `yaml:"order"`
	At           string           `yaml:"at"`
	Location     string           `yaml:"location"`
	Actor        string           `yaml:"actor"`
	Causes       []string         `yaml:"causes"`
	Participants []string         `yaml:"participants"`
	Observers    []string         `yaml:"observers"`
	Options      []decisionOption `yaml:"options"`
}

type decisionOption struct {
	ID             string         `yaml:"id"`
	Action         string         `yaml:"action"`
	Summary        string         `yaml:"summary"`
	PerceivedGoal  string         `yaml:"perceived_goal"`
	IntendedEffect string         `yaml:"intended_effect"`
	ActualEffect   string         `yaml:"actual_effect"`
	Delta          map[string]any `yaml:"delta"`
	Utility        map[string]int `yaml:"utility"`
}

type rejectedOption struct {
	OptionID string `json:"option_id"`
	Score    int    `json:"score"`
}

type event struct {
	EventID              string           `json:"event_id"`
	SourceID             string           `json:"source_id"`
	OccurredAt           string           `json:"occurred_at"`
	Location             string           `json:"location"`
	Actor                string           `json:"actor"`
	Participants         []string         `json:"participants"`
	Action               string           `json:"action"`
	Summary              string           `json:"summary"`
	PerceivedGoalByActor string           `json:"perceived_goal_by_actor"`
	CauseEventIDs        []string         `json:"cause_event_ids"`
	IntendedEffect       string           `json:"intended_effect"`
	ActualEffect         string           `json:"actual_effect"`
	WorldStateDelta      map[string]any   `json:"world_state_delta"`
	Observers            []string         `json:"observers"`
	Seeded               bool             `json:"seeded"`
	SelectedOption       string           `json:"selected_option,omitempty"`
	SelectedScore        int              `json:"selected_score,omitempty"`
	Utility              map[string]int   `json:"utility,omitempty"`
	RejectedOptions      []rejectedOption `json:"rejected_options,omitempty"`
}

type memory struct {
	Character        string   `json:"character"`
	ObservedEventIDs []string `json:"observed_event_ids"`
	Account          string   `json:"account"`
	Confidence       float64  `json:"confidence"`
	EmotionalWeight  string   `json:"emotional_weight"`
	Distortion       string   `json:"distortion"`
}

type stateDiff struct {
	Scenario string              `json:"scenario"`
	Before   map[string]any      `json:"before"`
	After    map[string]any      `json:"after"`
	Changes  []map[string]string `json:"changes"`
}

type runResult struct {
	Events    []event
	Memories  []memory
	StateDiff stateDiff
	Scene     string
	Chapter   string
	Summary   runSummary
}

type runSummary struct {
	ScenarioID        string `json:"scenario_id"`
	Day               int    `json:"day"`
	Title             string `json:"title"`
	ChapterTitle      string `json:"chapter_title"`
	POV               string `json:"pov"`
	EventCount        int    `json:"event_count"`
	SeededEvents      int    `json:"seeded_events"`
	EmergentEvents    int    `json:"emergent_events"`
	EndConditionMet   bool   `json:"end_condition_met"`
	RegistrationState string `json:"registration_state"`
}

type runIndexEntry struct {
	Day          int    `json:"day"`
	ScenarioID   string `json:"scenario_id"`
	Title        string `json:"title"`
	ChapterTitle string `json:"chapter_title"`
	POV          string `json:"pov"`
	Directory    string `json:"directory"`
	EventCount   int    `json:"event_count"`
	Excerpt      string `json:"excerpt"`
}

func main() {
	fs := flag.NewFlagSet("narrative-sim", flag.ExitOnError)
	bundleDir := fs.String("bundle", "narrative", "narrative bundle directory")
	scenarioPath := fs.String("scenario", "", "scenario YAML (default: <bundle>/scenarios/day-01.yaml)")
	outDir := fs.String("out", "", "output directory; omit to print the run summary")
	all := fs.Bool("all", false, "run all scenarios in day order and write a run index")
	_ = fs.Parse(os.Args[1:])
	if *all {
		if *outDir == "" {
			*outDir = filepath.Join(*bundleDir, "runs")
		}
		if err := runAll(*bundleDir, *outDir); err != nil {
			fatal(err)
		}
		return
	}

	if *scenarioPath == "" {
		*scenarioPath = filepath.Join(*bundleDir, "scenarios", "day-01.yaml")
	}
	b, err := loadBundle(*bundleDir, *scenarioPath)
	if err != nil {
		fatal(err)
	}
	result, err := run(b)
	if err != nil {
		fatal(err)
	}
	if *outDir == "" {
		if err := writeJSON(os.Stdout, result.Summary); err != nil {
			fatal(err)
		}
		return
	}
	if err := writeResult(*outDir, result); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %d events (%d emergent) to %s\n", result.Summary.EventCount, result.Summary.EmergentEvents, *outDir)
}

func loadBundle(dir, scenarioPath string) (bundle, error) {
	var constitution constitutionFile
	if err := loadYAML(filepath.Join(dir, "constitution.yaml"), &constitution); err != nil {
		return bundle{}, err
	}
	var cast castFile
	if err := loadYAML(filepath.Join(dir, "cast.yaml"), &cast); err != nil {
		return bundle{}, err
	}
	for _, required := range []string{"world.yaml", "dynamics.yaml"} {
		var document any
		if err := loadYAML(filepath.Join(dir, required), &document); err != nil {
			return bundle{}, err
		}
	}
	var s scenario
	if err := loadYAML(scenarioPath, &s); err != nil {
		return bundle{}, err
	}

	characters := make(map[string]string, len(cast.Characters))
	for _, character := range cast.Characters {
		if character.ID == "" || character.Name == "" {
			return bundle{}, errors.New("cast contains a character without id or name")
		}
		if _, exists := characters[character.ID]; exists {
			return bundle{}, fmt.Errorf("duplicate character id %q", character.ID)
		}
		characters[character.ID] = character.Name
	}
	if constitution.WorkTitle == "" || s.ID == "" || s.Title == "" || s.Day == 0 {
		return bundle{}, errors.New("constitution and scenario require titles and a scenario id")
	}
	for id := range s.InitialPrivate {
		if _, ok := characters[id]; !ok {
			return bundle{}, fmt.Errorf("scenario private state references unknown character %q", id)
		}
	}
	if len(s.SeedEvents) == 0 {
		return bundle{}, errors.New("scenario requires at least one seed event")
	}
	if len(s.DecisionPoints) == 0 {
		return bundle{}, errors.New("scenario requires at least one decision point")
	}
	if s.Chapter.Title == "" || s.Chapter.POV == "" {
		return bundle{}, errors.New("scenario requires chapter title and point of view")
	}
	return bundle{Title: constitution.WorkTitle, Characters: characters, Scenario: s}, nil
}

func loadYAML(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, value); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func run(b bundle) (runResult, error) {
	return runWithState(b, nil)
}

func runWithState(b bundle, inherited map[string]any) (runResult, error) {
	s := b.Scenario
	events, err := evaluateTimeline(s)
	if err != nil {
		return runResult{}, err
	}

	if err := validateLedger(events, b.Characters); err != nil {
		return runResult{}, err
	}
	memories := deriveMemories(events, s.MemoryCharacters)
	diff := deriveStateDiff(s, events, inherited)
	summary := runSummary{
		ScenarioID:        s.ID,
		Day:               s.Day,
		Title:             s.Title,
		ChapterTitle:      s.Chapter.Title,
		POV:               s.Chapter.POV,
		EventCount:        len(events),
		SeededEvents:      len(s.SeedEvents),
		EmergentEvents:    len(events) - len(s.SeedEvents),
		EndConditionMet:   true,
		RegistrationState: resultState(s.ResultStateKey, diff.After),
	}
	summary.EndConditionMet = summary.RegistrationState != ""
	return runResult{Events: events, Memories: memories, StateDiff: diff, Scene: sceneMaterial(b, events), Chapter: composeChapter(s, events), Summary: summary}, nil
}

type timelineItem struct {
	order    int
	seed     *seedEvent
	decision *decisionPoint
}

func evaluateTimeline(s scenario) ([]event, error) {
	items := make([]timelineItem, 0, len(s.SeedEvents)+len(s.DecisionPoints))
	for i := range s.SeedEvents {
		items = append(items, timelineItem{order: s.SeedEvents[i].Order, seed: &s.SeedEvents[i]})
	}
	for i := range s.DecisionPoints {
		items = append(items, timelineItem{order: s.DecisionPoints[i].Order, decision: &s.DecisionPoints[i]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].order < items[j].order })

	refs := make(map[string]string, len(items))
	events := make([]event, 0, len(items))
	for i, item := range items {
		id := fmt.Sprintf("E%03d", i+1)
		if item.seed != nil {
			s := item.seed
			if s.ID == "" || s.Order == 0 {
				return nil, errors.New("seed event requires id and non-zero order")
			}
			events = append(events, event{EventID: id, SourceID: s.ID, OccurredAt: s.At, Location: s.Location, Actor: s.Actor, Participants: s.Participants, Action: s.Action, Summary: s.Event, PerceivedGoalByActor: s.PerceivedGoal, CauseEventIDs: []string{}, IntendedEffect: s.IntendedEffect, ActualEffect: s.ActualEffect, WorldStateDelta: map[string]any{"director_source": s.DirectorSource}, Observers: s.Observers, Seeded: true})
			refs[s.ID] = id
			continue
		}
		d := item.decision
		selected, rejected, err := chooseOption(d.Options)
		if err != nil {
			return nil, fmt.Errorf("decision %s: %w", d.ID, err)
		}
		causes := make([]string, 0, len(d.Causes))
		for _, ref := range d.Causes {
			causeID, ok := refs[ref]
			if !ok {
				return nil, fmt.Errorf("decision %s has unavailable cause %s", d.ID, ref)
			}
			causes = append(causes, causeID)
		}
		events = append(events, event{EventID: id, SourceID: d.ID, OccurredAt: d.At, Location: d.Location, Actor: d.Actor, Participants: d.Participants, Action: selected.Action, Summary: selected.Summary, PerceivedGoalByActor: selected.PerceivedGoal, CauseEventIDs: causes, IntendedEffect: selected.IntendedEffect, ActualEffect: selected.ActualEffect, WorldStateDelta: selected.Delta, Observers: d.Observers, SelectedOption: selected.ID, SelectedScore: utilityScore(selected.Utility), Utility: selected.Utility, RejectedOptions: rejected})
		refs[d.ID] = id
	}
	return events, nil
}

func chooseOption(options []decisionOption) (decisionOption, []rejectedOption, error) {
	if len(options) == 0 {
		return decisionOption{}, nil, errors.New("no options")
	}
	sorted := append([]decisionOption(nil), options...)
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

func utilityScore(utility map[string]int) int {
	total := 0
	for _, value := range utility {
		total += value
	}
	return total
}

func resultState(key string, state map[string]any) string {
	if key == "" {
		return "complete"
	}
	value, ok := state[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

func validateLedger(events []event, characters map[string]string) error {
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		if e.EventID == "" || e.OccurredAt == "" || e.Location == "" || e.Action == "" || e.Summary == "" || e.PerceivedGoalByActor == "" || e.IntendedEffect == "" || e.ActualEffect == "" || e.WorldStateDelta == nil {
			return fmt.Errorf("event %q does not satisfy the event contract", e.EventID)
		}
		if seen[e.EventID] {
			return fmt.Errorf("duplicate event id %q", e.EventID)
		}
		for _, cause := range e.CauseEventIDs {
			if !seen[cause] {
				return fmt.Errorf("event %q references future or missing cause %q", e.EventID, cause)
			}
		}
		for _, id := range append(append([]string{}, e.Participants...), e.Observers...) {
			if id == "environment" {
				continue
			}
			if _, ok := characters[id]; !ok {
				return fmt.Errorf("event %q references unknown character %q", e.EventID, id)
			}
		}
		seen[e.EventID] = true
	}
	return nil
}

func deriveMemories(events []event, characters []string) []memory {
	observed := func(character string) []string {
		var ids []string
		for _, e := range events {
			if contains(e.Observers, character) {
				ids = append(ids, e.EventID)
			}
		}
		return ids
	}
	memories := make([]memory, 0, len(characters))
	for i, character := range characters {
		ids := observed(character)
		var accounts []string
		for _, e := range events {
			if contains(e.Observers, character) {
				accounts = append(accounts, e.Summary)
			}
		}
		memories = append(memories, memory{
			Character: character, ObservedEventIDs: ids, Account: strings.Join(accounts, " "),
			Confidence: 0.9 - float64(i)*0.07, EmotionalWeight: "取决于事件与其目标、债务和恐惧的距离。",
			Distortion: "只保存亲历和转述，并会用自己的目标重新解释原因。",
		})
	}
	return memories
}

func deriveStateDiff(s scenario, events []event, inherited map[string]any) stateDiff {
	before := cloneMap(inherited)
	for key, value := range s.InitialWorldState {
		before[key] = value
	}
	after := cloneMap(before)
	changes := make([]map[string]string, 0, len(events))
	for _, e := range events {
		for key, value := range e.WorldStateDelta {
			if key != "director_source" {
				after[key] = value
			}
		}
		changes = append(changes, map[string]string{"event_id": e.EventID, "change": e.ActualEffect})
	}
	return stateDiff{Scenario: s.ID, Before: before, After: after, Changes: changes}
}

func sceneMaterial(b bundle, events []event) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s · %s：场景候选材料\n\n", b.Title, b.Scenario.Chapter.Title)
	out.WriteString("> 这不是成稿。它只使用事实账中已经发生的材料，供后续选择视角、控制披露和文学改写。\n\n")
	out.WriteString("## 场景发动机\n\n")
	fmt.Fprintf(&out, "- 视角：%s。\n- 起始状态继承：%s。\n- 终止条件：%s。\n\n", b.Scenario.Chapter.POV, emptyAs(b.Scenario.Inherits, "无，第一日"), b.Scenario.EndCondition)
	out.WriteString("## 可用事实顺序\n\n")
	for _, e := range events {
		fmt.Fprintf(&out, "%d. **%s｜%s**：%s\n", eventNumber(e.EventID), e.OccurredAt, e.Location, e.Summary)
	}
	out.WriteString("\n## 分支记录\n\n")
	for _, e := range events {
		if e.SelectedOption != "" {
			fmt.Fprintf(&out, "- %s 选择 `%s`，另有 %d 个落选行动。\n", e.SourceID, e.SelectedOption, len(e.RejectedOptions))
		}
	}
	return out.String()
}

func composeChapter(s scenario, events []event) string {
	selected := make(map[string]string, len(events))
	for _, e := range events {
		selected[e.SourceID] = e.SelectedOption
	}
	paragraphs := []string{s.Chapter.Opening}
	for _, passage := range s.Chapter.Passages {
		option, exists := selected[passage.Source]
		if exists && (passage.Option == "" || passage.Option == option) {
			paragraphs = append(paragraphs, passage.Text)
		}
	}
	paragraphs = append(paragraphs, s.Chapter.Closing)
	var out strings.Builder
	fmt.Fprintf(&out, "# 第%d章 %s\n\n", s.Chapter.Number, s.Chapter.Title)
	for _, paragraph := range paragraphs {
		if strings.TrimSpace(paragraph) != "" {
			fmt.Fprintf(&out, "%s\n\n", strings.TrimSpace(paragraph))
		}
	}
	return out.String()
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func eventNumber(id string) int {
	var n int
	_, _ = fmt.Sscanf(id, "E%d", &n)
	return n
}

func writeResult(dir string, result runResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dir, "summary.json"), result.Summary); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dir, "memories.json"), result.Memories); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dir, "state-diff.json"), result.StateDiff); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "chapter.md"), []byte(result.Chapter), 0o644); err != nil {
		return err
	}
	ledger, err := os.Create(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		return err
	}
	w := bufio.NewWriter(ledger)
	for _, e := range result.Events {
		data, marshalErr := json.Marshal(e)
		if marshalErr != nil {
			_ = ledger.Close()
			return marshalErr
		}
		if _, err = fmt.Fprintln(w, string(data)); err != nil {
			_ = ledger.Close()
			return err
		}
	}
	if err = w.Flush(); err != nil {
		_ = ledger.Close()
		return err
	}
	if err = ledger.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "scene-material.md"), []byte(result.Scene), 0o644)
}

func runAll(bundleDir, outDir string) error {
	paths, err := filepath.Glob(filepath.Join(bundleDir, "scenarios", "day-*.yaml"))
	if err != nil || len(paths) == 0 {
		return errors.New("no day scenarios found")
	}
	type planned struct {
		bundle bundle
		path   string
	}
	plans := make([]planned, 0, len(paths))
	for _, path := range paths {
		b, loadErr := loadBundle(bundleDir, path)
		if loadErr != nil {
			return loadErr
		}
		plans = append(plans, planned{bundle: b, path: path})
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].bundle.Scenario.Day < plans[j].bundle.Scenario.Day })
	states := map[string]map[string]any{}
	index := make([]runIndexEntry, 0, len(plans))
	for _, plan := range plans {
		s := plan.bundle.Scenario
		var inherited map[string]any
		if s.Inherits != "" {
			var ok bool
			inherited, ok = states[s.Inherits]
			if !ok {
				return fmt.Errorf("scenario %s inherits unavailable run %s", s.ID, s.Inherits)
			}
		}
		result, runErr := runWithState(plan.bundle, inherited)
		if runErr != nil {
			return runErr
		}
		directory := fmt.Sprintf("day-%02d", s.Day)
		if err := writeResult(filepath.Join(outDir, directory), result); err != nil {
			return err
		}
		states[s.ID] = result.StateDiff.After
		index = append(index, runIndexEntry{Day: s.Day, ScenarioID: s.ID, Title: s.Title, ChapterTitle: s.Chapter.Title, POV: s.Chapter.POV, Directory: directory, EventCount: len(result.Events), Excerpt: chapterExcerpt(result.Chapter)})
	}
	if err := writeJSONFile(filepath.Join(outDir, "index.json"), index); err != nil {
		return err
	}
	fmt.Printf("wrote %d linked narrative days to %s\n", len(index), outDir)
	return nil
}

func chapterExcerpt(chapter string) string {
	text := strings.ReplaceAll(chapter, "\n", " ")
	text = strings.TrimSpace(text)
	if len([]rune(text)) > 100 {
		return string([]rune(text)[:100]) + "……"
	}
	return text
}

func writeJSONFile(path string, value any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := writeJSON(f, value); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeJSON(w *os.File, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "narrative-sim:", err)
	os.Exit(1)
}

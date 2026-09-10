package narrative

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/wanpengxie/atoll/protocol/actor"
)

type simulationSpec struct {
	Ticks              []tick   `yaml:"ticks"`
	Processes          []rule   `yaml:"environment_processes"`
	RecurringProcesses []rule   `yaml:"recurring_processes"`
	Policies           []rule   `yaml:"actor_policies"`
	Characters         []string `yaml:"-"`
}

type castSpec struct {
	Characters []struct {
		ID string `yaml:"id"`
	} `yaml:"characters"`
}

type tick struct {
	ID    string `yaml:"id"`
	Label string `yaml:"label"`
}

type rule struct {
	ID           string      `yaml:"id"`
	Tick         string      `yaml:"tick"`
	Order        int         `yaml:"order"`
	Mode         string      `yaml:"mode"`
	CooldownDays int         `yaml:"cooldown_days"`
	MaxFirings   int         `yaml:"max_firings"`
	Actor        string      `yaml:"actor"`
	Location     string      `yaml:"location"`
	When         []condition `yaml:"when"`
	Options      []option    `yaml:"options"`
	Participants []string    `yaml:"participants"`
	Observers    []string    `yaml:"observers"`
}

type condition struct {
	Key   string `yaml:"key" json:"key"`
	Op    string `yaml:"op" json:"op"`
	Value any    `yaml:"value" json:"value,omitempty"`
}

type option struct {
	ID             string         `yaml:"id"`
	Action         string         `yaml:"action"`
	Summary        string         `yaml:"summary"`
	PerceivedGoal  string         `yaml:"perceived_goal"`
	IntendedEffect string         `yaml:"intended_effect"`
	ActualEffect   string         `yaml:"actual_effect"`
	Effects        []effect       `yaml:"effects"`
	Utility        map[string]int `yaml:"utility"`
	Modifiers      []modifier     `yaml:"utility_modifiers"`
	Prose          string         `yaml:"prose"`
}

type modifier struct {
	When   []condition `yaml:"when"`
	Add    int         `yaml:"add"`
	Reason string      `yaml:"reason"`
}

type effect struct {
	Key   string `yaml:"key" json:"key"`
	Op    string `yaml:"op" json:"op"`
	Value any    `yaml:"value" json:"value,omitempty"`
}

type belief struct {
	Facts            map[string]any    `json:"facts"`
	Sources          map[string]string `json:"sources"`
	ObservedEventIDs []string          `json:"observed_event_ids"`
	UsedRules        map[string]int    `json:"used_rules"`
	RuleCounts       map[string]int    `json:"rule_counts"`
}

type fact struct {
	EventID              string         `json:"event_id"`
	Day                  int            `json:"day"`
	Tick                 string         `json:"tick"`
	RuleID               string         `json:"rule_id"`
	Location             string         `json:"location"`
	Actor                string         `json:"actor"`
	Participants         []string       `json:"participants"`
	Observers            []string       `json:"observers"`
	Action               string         `json:"action"`
	Summary              string         `json:"summary"`
	PerceivedGoalByActor string         `json:"perceived_goal_by_actor"`
	IntendedEffect       string         `json:"intended_effect"`
	ActualEffect         string         `json:"actual_effect"`
	WorldStateDelta      map[string]any `json:"world_state_delta"`
	SelectedOption       string         `json:"selected_option"`
	SelectedScore        int            `json:"selected_score"`
	Origin               string         `json:"origin"`
	NarrativeSeed        string         `json:"narrative_seed,omitempty"`
	CauseEventIDs        []string       `json:"cause_event_ids,omitempty"`
}

type proposal struct {
	RuleID         string         `json:"rule_id"`
	Actor          string         `json:"actor"`
	Tick           string         `json:"tick"`
	SelectedOption string         `json:"selected_option"`
	Action         string         `json:"action"`
	Goal           string         `json:"goal,omitempty"`
	ExpectedEffect string         `json:"expected_effect,omitempty"`
	Preconditions  []condition    `json:"preconditions,omitempty"`
	KnownFacts     map[string]any `json:"known_facts"`
}

type decision struct {
	Day            int    `json:"day"`
	Tick           string `json:"tick"`
	RuleID         string `json:"rule_id"`
	Actor          string `json:"actor"`
	SelectedOption string `json:"selected_option,omitempty"`
	Action         string `json:"action,omitempty"`
	Goal           string `json:"goal,omitempty"`
	ExpectedEffect string `json:"expected_effect,omitempty"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	Origin         string `json:"origin"`
}

type persistedWorld struct {
	Day          int               `json:"day"`
	State        map[string]any    `json:"state"`
	Seq          int               `json:"seq"`
	FiredRules   map[string]int    `json:"fired_rules"`
	RuleCounts   map[string]int    `json:"rule_counts"`
	StateSources map[string]string `json:"state_sources,omitempty"`
	Auto         autoRunState      `json:"auto_run"`
}

type autoRunState struct {
	Enabled      bool   `json:"enabled"`
	IntervalMS   int64  `json:"interval_ms,omitempty"`
	Through      int    `json:"through,omitempty"`
	MaxEmptyDays int    `json:"max_empty_days,omitempty"`
	EmptyDays    int    `json:"empty_days,omitempty"`
	TimerID      string `json:"timer_id,omitempty"`
}

type runIndexEntry struct {
	Day       int    `json:"day"`
	Directory string `json:"directory"`
}

func loadSimulation(bundle string) (simulationSpec, error) {
	var spec simulationSpec
	if err := loadYAML(filepath.Join(bundle, "simulation.yaml"), &spec); err != nil {
		return simulationSpec{}, err
	}
	var cast castSpec
	if err := loadYAML(filepath.Join(bundle, "cast.yaml"), &cast); err != nil {
		return simulationSpec{}, err
	}
	for _, character := range cast.Characters {
		if character.ID != "" {
			spec.Characters = append(spec.Characters, character.ID)
		}
	}
	if len(spec.Ticks) == 0 || len(spec.Policies) == 0 {
		return simulationSpec{}, fmt.Errorf("narrative: simulation requires ticks and actor policies")
	}
	spec.Processes = append(spec.Processes, spec.RecurringProcesses...)
	if err := validateSimulation(spec); err != nil {
		return simulationSpec{}, err
	}
	return spec, nil
}

func validateSimulation(spec simulationSpec) error {
	ticks := map[string]bool{}
	for _, tick := range spec.Ticks {
		if tick.ID == "" || ticks[tick.ID] {
			return fmt.Errorf("narrative: tick ids must be non-empty and unique: %q", tick.ID)
		}
		ticks[tick.ID] = true
	}
	ids := map[string]bool{}
	characters := map[string]bool{}
	for _, character := range spec.Characters {
		if characters[character] {
			return fmt.Errorf("narrative: character ids must be unique: %q", character)
		}
		characters[character] = true
	}
	all := append(append([]rule(nil), spec.Processes...), spec.Policies...)
	for _, candidate := range all {
		if candidate.ID == "" || ids[candidate.ID] {
			return fmt.Errorf("narrative: rule ids must be non-empty and unique: %q", candidate.ID)
		}
		ids[candidate.ID] = true
		if !ticks[candidate.Tick] || candidate.Actor == "" || len(candidate.Options) == 0 {
			return fmt.Errorf("narrative: rule %s has invalid tick, actor, or options", candidate.ID)
		}
		if candidate.Actor != "environment" && !characters[candidate.Actor] {
			return fmt.Errorf("narrative: rule %s references character %q absent from cast", candidate.ID, candidate.Actor)
		}
		if candidate.Mode != "" && candidate.Mode != "once" && candidate.Mode != "recurring" {
			return fmt.Errorf("narrative: rule %s has unsupported mode %q", candidate.ID, candidate.Mode)
		}
		if candidate.CooldownDays < 0 || candidate.MaxFirings < 0 {
			return fmt.Errorf("narrative: rule %s has negative recurrence limits", candidate.ID)
		}
		for _, condition := range candidate.When {
			if !validConditionOp(condition.Op) {
				return fmt.Errorf("narrative: rule %s has unsupported condition %q", candidate.ID, condition.Op)
			}
		}
		for _, option := range candidate.Options {
			for _, effect := range option.Effects {
				if !validEffectOp(effect.Op) {
					return fmt.Errorf("narrative: rule %s has unsupported effect %q", candidate.ID, effect.Op)
				}
			}
			for _, modifier := range option.Modifiers {
				for _, condition := range modifier.When {
					if !validConditionOp(condition.Op) {
						return fmt.Errorf("narrative: rule %s has unsupported modifier condition %q", candidate.ID, condition.Op)
					}
				}
			}
		}
	}
	return nil
}

func validConditionOp(op string) bool {
	switch op {
	case "exists", "not_exists", "eq", "neq", "gt", "gte", "lt", "lte":
		return true
	default:
		return false
	}
}

func validEffectOp(op string) bool {
	return op == "set" || op == "add" || op == "multiply" || op == "delete"
}

func loadInitialWorld(bundle string, startDay int) (persistedWorld, error) {
	var index []runIndexEntry
	if err := loadJSON(filepath.Join(bundle, "runs", "index.json"), &index); err != nil {
		return persistedWorld{}, err
	}
	if len(index) == 0 {
		return persistedWorld{}, fmt.Errorf("narrative: run index is empty")
	}
	latest, err := indexAtDay(index, startDay)
	if err != nil {
		return persistedWorld{}, err
	}
	var diff struct {
		After map[string]any `json:"after"`
	}
	if err := loadJSON(filepath.Join(bundle, "runs", latest.Directory, "state-diff.json"), &diff); err != nil {
		return persistedWorld{}, err
	}
	world := persistedWorld{Day: latest.Day, State: cloneMap(diff.After), FiredRules: map[string]int{}, RuleCounts: map[string]int{}, StateSources: map[string]string{}}
	for _, entry := range index {
		if entry.Day > startDay {
			break
		}
		var rows []struct {
			EventID         string         `json:"event_id"`
			SourceID        string         `json:"source_id"`
			WorldStateDelta map[string]any `json:"world_state_delta"`
		}
		if err := loadJSONLines(filepath.Join(bundle, "runs", entry.Directory, "ledger.jsonl"), func(raw []byte) error {
			var row struct {
				EventID         string         `json:"event_id"`
				SourceID        string         `json:"source_id"`
				WorldStateDelta map[string]any `json:"world_state_delta"`
			}
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			rows = append(rows, row)
			return nil
		}); err != nil {
			return persistedWorld{}, err
		}
		for _, row := range rows {
			for key := range row.WorldStateDelta {
				if key != "director_source" {
					world.StateSources[key] = fmt.Sprintf("day-%02d/%s", entry.Day, row.EventID)
				}
			}
			if id := strings.TrimPrefix(row.SourceID, "generated/"); id != row.SourceID {
				world.FiredRules[id] = entry.Day
				world.RuleCounts[id]++
			}
		}
	}
	return world, nil
}

func loadInitialBelief(bundle, character string, startDay int) (belief, error) {
	var index []runIndexEntry
	if err := loadJSON(filepath.Join(bundle, "runs", "index.json"), &index); err != nil {
		return belief{}, err
	}
	if len(index) == 0 {
		return belief{}, fmt.Errorf("narrative: run index is empty")
	}
	if _, err := indexAtDay(index, startDay); err != nil {
		return belief{}, err
	}
	out := belief{Facts: map[string]any{}, Sources: map[string]string{}, UsedRules: map[string]int{}, RuleCounts: map[string]int{}}
	for _, entry := range index {
		if entry.Day > startDay {
			break
		}
		if err := loadJSONLines(filepath.Join(bundle, "runs", entry.Directory, "ledger.jsonl"), func(raw []byte) error {
			var event struct {
				EventID         string         `json:"event_id"`
				SourceID        string         `json:"source_id"`
				Actor           string         `json:"actor"`
				Summary         string         `json:"summary"`
				Observers       []string       `json:"observers"`
				WorldStateDelta map[string]any `json:"world_state_delta"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				return err
			}
			if contains(event.Observers, character) {
				ref := fmt.Sprintf("day-%02d/%s", entry.Day, event.EventID)
				out.ObservedEventIDs = append(out.ObservedEventIDs, ref)
				for key, value := range event.WorldStateDelta {
					if key == "director_source" {
						continue
					}
					out.Facts[key] = value
					out.Sources[key] = ref + " · " + event.Summary
				}
			}
			if event.Actor == character {
				if id := strings.TrimPrefix(event.SourceID, "generated/"); id != event.SourceID {
					out.UsedRules[id] = entry.Day
					out.RuleCounts[id]++
				}
			}
			return nil
		}); err != nil {
			return belief{}, err
		}
	}
	return out, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func indexAtDay(index []runIndexEntry, day int) (runIndexEntry, error) {
	for _, entry := range index {
		if entry.Day == day {
			return entry, nil
		}
	}
	return runIndexEntry{}, fmt.Errorf("narrative: run index has no day %d migration point", day)
}

func loadYAML(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func loadJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func loadJSONLines(path string, visit func([]byte) error) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := visit([]byte(line)); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return nil
}

func rulesForTick(rules []rule, tickID string) []rule {
	out := make([]rule, 0)
	for _, candidate := range rules {
		if candidate.Tick == tickID {
			out = append(out, candidate)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].ID < out[j].ID
		}
		return out[i].Order < out[j].Order
	})
	return out
}

func conditionsMatch(state map[string]any, conditions []condition) bool {
	for _, c := range conditions {
		actual, exists := state[c.Key]
		switch c.Op {
		case "exists":
			if !exists {
				return false
			}
		case "eq":
			if !exists || fmt.Sprint(actual) != fmt.Sprint(c.Value) {
				return false
			}
		case "gte":
			if !exists || number(actual) < number(c.Value) {
				return false
			}
		case "gt":
			if !exists || number(actual) <= number(c.Value) {
				return false
			}
		case "lte":
			if !exists || number(actual) > number(c.Value) {
				return false
			}
		case "lt":
			if !exists || number(actual) >= number(c.Value) {
				return false
			}
		case "neq":
			if exists && fmt.Sprint(actual) == fmt.Sprint(c.Value) {
				return false
			}
		case "not_exists":
			if exists {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func choose(options []option, state map[string]any) (option, error) {
	if len(options) == 0 {
		return option{}, fmt.Errorf("narrative: rule has no options")
	}
	copyOf := append([]option(nil), options...)
	sort.Slice(copyOf, func(i, j int) bool {
		left, right := optionScore(copyOf[i], state), optionScore(copyOf[j], state)
		if left == right {
			return copyOf[i].ID < copyOf[j].ID
		}
		return left > right
	})
	return copyOf[0], nil
}

func optionScore(candidate option, state map[string]any) int {
	total := score(candidate.Utility)
	for _, modifier := range candidate.Modifiers {
		if conditionsMatch(state, modifier.When) {
			total += modifier.Add
		}
	}
	return total
}

func canFire(candidate rule, lastDay, count, day int) bool {
	if count == 0 && lastDay != 0 {
		count = 1
	}
	if candidate.Mode != "recurring" {
		return count == 0
	}
	if candidate.MaxFirings > 0 && count >= candidate.MaxFirings {
		return false
	}
	cooldown := candidate.CooldownDays
	if cooldown < 1 {
		cooldown = 1
	}
	return lastDay == 0 || day-lastDay >= cooldown
}

func score(utility map[string]int) int {
	total := 0
	for _, value := range utility {
		total += value
	}
	return total
}

func applyEffects(state map[string]any, effects []effect) map[string]any {
	delta := map[string]any{}
	for _, e := range effects {
		switch e.Op {
		case "set":
			state[e.Key] = e.Value
		case "add":
			state[e.Key] = number(state[e.Key]) + number(e.Value)
		case "multiply":
			state[e.Key] = number(state[e.Key]) * number(e.Value)
		case "delete":
			delete(state, e.Key)
		}
		delta[e.Key] = state[e.Key]
	}
	return delta
}

func number(value any) float64 {
	switch n := value.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	case json.Number:
		v, _ := n.Float64()
		return v
	default:
		var v float64
		_, _ = fmt.Sscan(fmt.Sprint(value), &v)
		return v
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneStrings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneInts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func memberSeed(character string) string {
	return "narrative-" + strings.ReplaceAll(character, "_", "-")
}

func peerTarget(prefix, character string) actor.ActorID {
	return actor.ActorID("peer:c0." + prefix + "-" + strings.ReplaceAll(character, "_", "-"))
}

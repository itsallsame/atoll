package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunIsDeterministicAndMeetsEndCondition(t *testing.T) {
	b := testBundle(t)
	first, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	second, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same bundle produced different histories")
	}
	if !first.Summary.EndConditionMet || first.Summary.RegistrationState != "attached_pending" {
		t.Fatalf("unexpected ending: %+v", first.Summary)
	}
	if first.Summary.EmergentEvents < 3 {
		t.Fatalf("only %d emergent events", first.Summary.EmergentEvents)
	}
}

func TestEveryEventHasPastCausesAndKnownObservers(t *testing.T) {
	b := testBundle(t)
	result, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLedger(result.Events, b.Characters); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateDocumentComparisonDoesNotLeakToAbsentCharacters(t *testing.T) {
	b := testBundle(t)
	result, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range result.Events {
		if e.SourceID != "decision_fragment" {
			continue
		}
		for _, absent := range []string{"he_asheng", "lu_jinghe", "zhou_jiren", "shen_boan"} {
			if contains(e.Observers, absent) {
				t.Fatalf("private comparison leaked to %s", absent)
			}
		}
		return
	}
	t.Fatal("missing decision_fragment")
}

func TestChangingCharacterUtilitiesChangesTheHistory(t *testing.T) {
	b := testBundle(t)
	for i := range b.Scenario.DecisionPoints {
		decision := &b.Scenario.DecisionPoints[i]
		if decision.ID != "decision_fragment" {
			continue
		}
		for j := range decision.Options {
			if decision.Options[j].ID == "disclose_immediately" {
				decision.Options[j].Utility["new_pressure"] = 20
			}
		}
	}
	result, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range result.Events {
		if e.SourceID == "decision_fragment" {
			if e.SelectedOption != "disclose_immediately" {
				t.Fatalf("selected %q after utility change", e.SelectedOption)
			}
			return
		}
	}
	t.Fatal("missing decision_fragment")
}

func TestTimelineInterleavesSeedsAndDecisionsByOrder(t *testing.T) {
	result, err := run(testBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"seed_lin_claim", "seed_he_claim", "seed_rain_rumor", "decision_fragment", "decision_lu_evidence", "seed_zhou_arrives", "decision_zhou_process", "decision_boundary", "decision_he_testimony", "decision_registration"}
	var got []string
	for _, e := range result.Events {
		got = append(got, e.SourceID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("timeline order = %v, want %v", got, want)
	}
}

func TestMemoriesOnlyReferenceObservedEvents(t *testing.T) {
	b := testBundle(t)
	result, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	events := make(map[string]event)
	for _, e := range result.Events {
		events[e.EventID] = e
	}
	for _, m := range result.Memories {
		for _, id := range m.ObservedEventIDs {
			if !contains(events[id].Observers, m.Character) {
				t.Fatalf("%s remembers unobserved event %s", m.Character, id)
			}
		}
	}
}

func TestResultRemainsJSONSerializable(t *testing.T) {
	result, err := run(testBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Scene, "分支记录") {
		t.Fatal("scene material lacks branch record")
	}
	if !strings.Contains(result.Chapter, "第一张挂号") {
		t.Fatal("run lacks composed chapter")
	}
}

func TestRunAllCarriesDayOneStateIntoDayTwo(t *testing.T) {
	out := t.TempDir()
	if err := runAll("../../narrative", out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "day-02", "state-diff.json"))
	if err != nil {
		t.Fatal(err)
	}
	var diff stateDiff
	if err := json.Unmarshal(data, &diff); err != nil {
		t.Fatal(err)
	}
	if got := diff.Before["registration.south_field_7"]; got != "attached_pending" {
		t.Fatalf("day two inherited registration=%v", got)
	}
	if got := diff.After["claimant.lin_suyun.recorded_as"]; got != "林素云" {
		t.Fatalf("day two claimant name=%v", got)
	}
	if _, err := os.Stat(filepath.Join(out, "index.json")); err != nil {
		t.Fatal(err)
	}
}

func TestContinuationGeneratesDayThreeWithoutScenarioFile(t *testing.T) {
	out := t.TempDir()
	if err := runAll("../../narrative", out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("../../narrative/scenarios/day-03.yaml"); !os.IsNotExist(err) {
		t.Fatalf("day three must not come from a scenario file: %v", err)
	}
	if err := continueRuns("../../narrative", out, 1); err != nil {
		t.Fatal(err)
	}

	var index []runIndexEntry
	data, err := os.ReadFile(filepath.Join(out, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	if len(index) != 3 || index[2].ScenarioID != "generated-day-03" {
		t.Fatalf("unexpected continuation index: %+v", index)
	}

	data, err = os.ReadFile(filepath.Join(out, "day-03", "state-diff.json"))
	if err != nil {
		t.Fatal(err)
	}
	var diff stateDiff
	if err := json.Unmarshal(data, &diff); err != nil {
		t.Fatal(err)
	}
	if got := diff.Before["claimant.lin_suyun.recorded_as"]; got != "林素云" {
		t.Fatalf("generated day lost inherited identity: %v", got)
	}
	if got := diff.After["disease.temple_cases"]; got != float64(5) {
		t.Fatalf("disease process did not advance: %v", got)
	}
	if got := diff.After["lin_suyun.exit"]; got != "departed_with_recorded_name" {
		t.Fatalf("actor policies did not converge on departure: %v", got)
	}
	if _, err := os.Stat(filepath.Join(out, "day-03", "chapter.md")); err != nil {
		t.Fatal(err)
	}

	data, err = os.ReadFile(filepath.Join(out, "day-03", "beliefs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var beliefs beliefLedger
	if err := json.Unmarshal(data, &beliefs); err != nil {
		t.Fatal(err)
	}
	if _, leaked := beliefs.Characters["lu_jinghe"].Facts["claimant.lin_suyun.recorded_as"]; leaked {
		t.Fatal("Lu Jinghe learned a name registration event he never observed")
	}

	data, err = os.ReadFile(filepath.Join(out, "day-03", "proposals.json"))
	if err != nil {
		t.Fatal(err)
	}
	var proposals []actionProposal
	if err := json.Unmarshal(data, &proposals); err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 4 {
		t.Fatalf("got %d actor proposals, want 4", len(proposals))
	}
	if proposals[0].Actor != "he_asheng" || len(proposals[0].KnownFacts) != 3 {
		t.Fatalf("first proposal does not expose its private decision input: %+v", proposals[0])
	}
	if got := proposals[0].KnownFacts["ferryman.claim"]; got != "one_day_grain" {
		t.Fatalf("proposal captured post-action rather than pre-action belief: %v", got)
	}
	if got := proposals[1].KnownFacts["lin_suyun.exit"]; got != "blocked" {
		t.Fatalf("Lin proposal did not preserve decision-time belief: %v", got)
	}
}

func TestActorCannotUseAnUnobservedOpportunity(t *testing.T) {
	var spec continuationSpec
	if err := loadYAML("../../narrative/simulation.yaml", &spec); err != nil {
		t.Fatal(err)
	}
	for i := range spec.Processes {
		if spec.Processes[i].ID == "temple_case_progression" {
			spec.Processes[i].Observers = []string{"shen_yanqiu", "zhou_jiren"}
		}
	}
	world := map[string]any{"disease.temple_cases": 4, "he_household.food_today": "none", "ferryman.claim": "one_day_grain"}
	beliefs := map[string]characterBelief{
		"he_asheng": {Facts: map[string]any{"he_household.food_today": "none", "ferryman.claim": "one_day_grain"}, Sources: map[string]string{}},
	}
	result, err := runContinuationDay(spec, 3, world, beliefs)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range result.Events {
		if e.Actor == "he_asheng" {
			t.Fatal("He Asheng acted on a transport opportunity he never observed")
		}
	}
	if got := result.StateDiff.After["patient.transport"]; got != "needed_before_noon" {
		t.Fatalf("world opportunity should still exist, got %v", got)
	}
}

func testBundle(t *testing.T) bundle {
	t.Helper()
	b, err := loadBundle("../../narrative", "../../narrative/scenarios/day-01.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
